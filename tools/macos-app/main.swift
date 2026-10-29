import AppKit
import WebKit
import UserNotifications

final class App: NSObject, NSApplicationDelegate, UNUserNotificationCenterDelegate, WKNavigationDelegate {
    let status = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    let popover = NSPopover()
    let web = WKWebView()
    var window: NSWindow!
    var inboxWindow: NSWindow!
    var inboxStack = NSStackView()
    var renderedInboxIDs: [String]?
    var stack = NSStackView()
    var process: Process?
    var timer: Timer?
    var base: URL?
    var records: [TaskRecord] = []
    var selected: String?
    var pending: Set<String> = []
    var busy = false
    var notice = "Loading Host records…"
    var permission = "Notifications: checking system preferences…"
    var journal = InboxJournal()
    var journalURL: URL!
    let launchedAt = ISO8601DateFormatter().string(from: Date())
    var destination: String?
    var destinationKey: String?
    let center = UNUserNotificationCenter.current()
    var completions: Bool {
        get { UserDefaults.standard.bool(forKey: "completionNotifications") }
        set { UserDefaults.standard.set(newValue, forKey: "completionNotifications") }
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        let menu = NSMenu()
        let appMenu = NSMenuItem(); menu.addItem(appMenu)
        appMenu.submenu = NSMenu()
        appMenu.submenu?.addItem(withTitle: "Quit Agent-Host", action: #selector(NSApplication.terminate(_:)), keyEquivalent: "q")
        NSApp.mainMenu = menu
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1200, height: 800),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "Agent-Host Workspace"; window.contentView = web; window.isReleasedWhenClosed = false
        window.center(); web.navigationDelegate = self
        web.loadHTMLString("<h1>Agent-Host</h1><p>Loading local workspace. Use the AGH menu bar entry for startup diagnostics.</p>", baseURL: nil)
        let controller = NSViewController(); controller.view = NSView(frame: NSRect(x: 0, y: 0, width: 430, height: 560))
        stack = configureStack(in: controller.view)
        popover.contentViewController = controller; popover.behavior = .transient; popover.animates = false
        status.button?.target = self; status.button?.action = #selector(togglePopover)
        center.delegate = self
        let category = UNNotificationCategory(identifier: "TASK", actions: [UNNotificationAction(identifier: "OPEN", title: "Open task", options: .foreground)], intentIdentifiers: [])
        center.setNotificationCategories([category])
        do {
            let support = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
                .appendingPathComponent("Agent-Host", isDirectory: true)
            try FileManager.default.createDirectory(at: support, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            // Each configured source set has its own journal, including across rebuilds.
            let name = Bundle.main.object(forInfoDictionaryKey: "AGHJournalID") as? String ?? "default"
            journalURL = support.appendingPathComponent("inbox-\(name).json")
            if FileManager.default.fileExists(atPath: journalURL.path) {
                journal = try JSONDecoder().decode(InboxJournal.self, from: Data(contentsOf: journalURL))
            }
            try startViewer()
        } catch { notice = "Startup failed: \(error.localizedDescription). Rebuild the app with valid viewer configuration." }
        render(); openWorkspace()
        center.requestAuthorization(options: [.alert, .badge]) { _, _ in self.refreshPermission() }
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { _ in self.refresh() }
    }

    func configureStack(in view: NSView) -> NSStackView {
        let scroll = NSScrollView(frame: view.bounds); scroll.autoresizingMask = [.width, .height]; scroll.hasVerticalScroller = true
        let result = NSStackView(); result.orientation = .vertical; result.alignment = .leading; result.spacing = 10
        result.edgeInsets = NSEdgeInsets(top: 16, left: 16, bottom: 16, right: 16)
        scroll.documentView = result; view.addSubview(scroll); result.translatesAutoresizingMaskIntoConstraints = false
        result.widthAnchor.constraint(equalTo: scroll.contentView.widthAnchor).isActive = true
        return result
    }
    func text(_ value: String, in target: NSStackView) {
        let label = NSTextField(wrappingLabelWithString: value); label.isSelectable = true
        target.addArrangedSubview(label)
        label.widthAnchor.constraint(lessThanOrEqualTo: target.widthAnchor, constant: -32).isActive = true
    }
    func button(_ title: String, action: Selector, in target: NSStackView, key: String? = nil, enabled: Bool = true) {
        let button = NSButton(title: title, target: self, action: action); button.isEnabled = enabled
        button.setAccessibilityLabel(title)
        if let key { button.identifier = NSUserInterfaceItemIdentifier(key) }
        target.addArrangedSubview(button)
    }
    func clear(_ target: NSStackView) { target.arrangedSubviews.forEach { target.removeArrangedSubview($0); $0.removeFromSuperview() } }

    func startViewer() throws {
        guard let resources = Bundle.main.resourceURL,
              let python = Bundle.main.object(forInfoDictionaryKey: "AGHPython") as? String,
              let arguments = Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] else {
            throw NSError(domain: "Agent-Host", code: 1, userInfo: [NSLocalizedDescriptionKey: "Missing app configuration"])
        }
        let child = Process(); child.executableURL = URL(fileURLWithPath: python)
        child.arguments = ["-B", resources.appendingPathComponent("tools/run-viewer/server.py").path] + arguments + ["--port", "0", "--no-reports", "--host-binary", resources.appendingPathComponent("bin/agent-host").path]
        child.currentDirectoryURL = resources
        let pipe = Pipe(); child.standardOutput = pipe
        var buffered = Data()
        pipe.fileHandleForReading.readabilityHandler = { handle in
            let data = handle.availableData
            if data.isEmpty { handle.readabilityHandler = nil; return }
            buffered.append(data)
            if let output = String(data: buffered, encoding: .utf8), let line = output.components(separatedBy: "\n").first(where: { $0.hasPrefix("Execution viewer: ") }),
               let url = URL(string: String(line.dropFirst("Execution viewer: ".count))) {
                handle.readabilityHandler = nil
                DispatchQueue.main.async { self.base = url; self.web.load(URLRequest(url: url)); self.refresh() }
            }
        }
        child.terminationHandler = { _ in DispatchQueue.main.async {
            self.base = nil; self.notice = "Workspace service exited. Quit and relaunch; inspect launch logs if it persists."; self.render()
        } }
        try child.run(); process = child
    }

    func get(_ path: String, completion: @escaping (Result<[String: Any], Error>) -> Void) {
        guard let base else { return }
        URLSession.shared.dataTask(with: URLRequest(url: URL(string: path, relativeTo: base)!, timeoutInterval: 10)) { data, response, error in
            let result: Result<[String: Any], Error> = Result {
                if let error { throw error }
                guard (response as? HTTPURLResponse)?.statusCode == 200, let data,
                      let value = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
                    throw NSError(domain: "Host", code: 1, userInfo: [NSLocalizedDescriptionKey: "Host response unavailable"])
                }
                return value
            }
            DispatchQueue.main.async { completion(result) }
        }.resume()
    }
    func refresh() {
        guard base != nil, !busy else { return }; busy = true
        get("/api/records") { result in
            switch result {
            case .failure(let error): self.busy = false; self.notice = "Offline: \(error.localizedDescription). Check Host configuration; reconnecting automatically."; self.render()
            case .success(let index):
                let rows = index["records"] as? [[String: Any]] ?? []
                let sources = index["sources"] as? [[String: Any]] ?? []
                self.notice = sources.contains { ["read_failed", "missing_database"].contains($0["status"] as? String ?? "") }
                    ? "A Host source is unavailable. Check its configuration and execution records." : ""
                if sources.contains(where: { $0["snapshot"] as? Bool == true }) {
                    self.notice = "Host snapshots may lag current writes. Open Workspace for source diagnostics; live controls are unavailable."
                }
                let reusable = self.cachedRecords(rows)
                let cachedKeys = Set(reusable.map { $0.key })
                let keys = rows.compactMap { $0["key"] as? String }.filter { !cachedKeys.contains($0) }
                self.loadRecords(keys, loaded: reusable)
            }
        }
    }
    func cachedRecords(_ rows: [[String: Any]]) -> [TaskRecord] {
        records.filter { record in
            guard !record.active, record.events.contains(where: { $0["type"] as? String == "run.finished" }),
                  let row = rows.first(where: { $0["key"] as? String == record.key }) else { return false }
            let defaults = ["execution_status": "unknown", "acceptance_status": "unverified", "memory_status": "", "experience_status": ""]
            for (field, fallback) in defaults {
                if row[field] as? String != (record.run[field] as? String ?? fallback) { return false }
            }
            return true
        }
    }
    func loadRecords(_ keys: [String], loaded: [TaskRecord]) {
        guard let key = keys.first else {
            records = loaded; busy = false
            if selected == nil { selected = records.first(where: { $0.active })?.key ?? records.first?.key }
            let fresh = journal.ingest(records.flatMap { $0.inbox })
            do {
                try JSONEncoder().encode(journal).write(to: journalURL, options: [.atomic])
                try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: journalURL.path)
                // Populate historical inbox without flooding macOS on first launch.
                fresh.filter { $0.at.prefix(19) >= launchedAt.prefix(19) }.forEach(notify)
            } catch { notice = "Inbox could not be saved: \(error.localizedDescription). Check Application Support permissions." }
            pending = pending.filter { key in records.contains { $0.key == key && $0.active } }
            render(); return
        }
        var query = URLComponents(); query.queryItems = [URLQueryItem(name: "key", value: key)]
        get("/api/record?" + query.percentEncodedQuery!) { result in
            var next = loaded
            switch result {
            case .success(let value):
                next.append(TaskRecord(key: key, run: value["run"] as? [String: Any] ?? [:], events: value["events"] as? [[String: Any]] ?? [], control: value["control"] as? [String: Any] ?? [:]))
            case .failure: self.notice = "Some records are unavailable. Open Workspace to inspect sources; reconnecting automatically."
            }
            self.loadRecords(Array(keys.dropFirst()), loaded: next)
        }
    }
    func refreshPermission() {
        center.getNotificationSettings { settings in DispatchQueue.main.async {
            self.permission = settings.authorizationStatus == .denied ? "Notifications denied. Enable Agent-Host in System Settings → Notifications. Inbox remains available." : "Notifications follow macOS preferences. Inbox is always available."
            self.render()
        } }
    }
    func notify(_ event: InboxEvent) {
        guard event.shouldNotify(completions: completions) else { return }
        let content = UNMutableNotificationContent(); content.title = "Agent-Host · \(event.kind)"
        content.body = event.title; content.categoryIdentifier = "TASK"; content.userInfo = ["key": event.key]
        // No sound, including configurable completion alerts.
        center.add(UNNotificationRequest(identifier: event.id, content: content, trigger: nil)) { error in
            if let error { DispatchQueue.main.async { self.permission = "Notification delivery unavailable: \(error.localizedDescription). Use Inbox."; self.render() } }
        }
    }
    func render() {
        let focused = popover.contentViewController?.view.window?.firstResponder as? NSButton
        let focusTitle = focused?.title
        let focusID = focused?.identifier
        let attention = records.contains { ["failed", "unknown"].contains($0.status) || $0.inbox.contains(where: { $0.kind == "input-needed" }) }
        let state = !notice.isEmpty ? "Unavailable" : attention ? "Attention needed" : records.contains(where: { $0.active }) ? "Running" : "Idle"
        status.button?.title = "AGH · \(state)"; status.button?.setAccessibilityLabel("Agent-Host: \(state)")
        clear(stack)
        text("Agent-Host · \(state)", in: stack)
        if !notice.isEmpty { text(notice, in: stack) }
        if let record = records.first(where: { $0.key == selected }) {
            text("\(record.title)\nAgent: \(record.agent)\n\(record.key)\n\(record.progress)\n\(record.outcomes)", in: stack)
            button("Open selected task", action: #selector(selectTask(_:)), in: stack, key: record.key)
            button(pending.contains(record.key) ? "Stop requested · waiting for Host outcome" : "Stop selected execution", action: #selector(stopSelected), in: stack,
                   enabled: notice.isEmpty && record.canStop && !pending.contains(record.key))
            if !record.canStop { text(record.control["reason"] as? String ?? "Control unavailable. Inspect the original Host entry.", in: stack) }
        } else {
            text(selected == nil ? "No execution records. Choose New Task to start a conversation." : "Selected record unavailable: \(selected!). Open Workspace to inspect its source, or select an available active execution.", in: stack)
        }
        for record in records.filter({ $0.active && $0.key != selected }) {
            button("Select active: \(record.title) · \(record.agent)", action: #selector(chooseActive(_:)), in: stack, key: record.key)
        }
        button("Open Workspace", action: #selector(openWorkspace), in: stack)
        button("New Task", action: #selector(newTask), in: stack, enabled: base != nil && web.url?.host == base?.host && !web.isLoading)
        button("Inbox (\(journal.entries.count))", action: #selector(openInbox), in: stack)
        for event in journal.entries.prefix(4) { button("\(event.kind): \(event.title)", action: #selector(selectTask(_:)), in: stack, key: event.key) }
        text(permission, in: stack)
        button(completions ? "Completion alerts: on (quiet)" : "Completion alerts: off", action: #selector(toggleCompletions), in: stack)
        if let focusTitle, let replacement = stack.arrangedSubviews.compactMap({ $0 as? NSButton }).first(where: { $0.title == focusTitle && $0.identifier == focusID }) {
            replacement.window?.makeFirstResponder(replacement)
        }
        if inboxWindow != nil { renderInbox() }
    }
    @objc func toggleCompletions() { completions.toggle(); render() }
    @objc func togglePopover() {
        refreshPermission()
        if popover.isShown { popover.performClose(nil) }
        else if let button = status.button { popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY) }
    }
    @objc func openWorkspace() { window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true); popover.performClose(nil) }
    @objc func chooseActive(_ sender: NSButton) { selected = sender.identifier?.rawValue; render() }
    @objc func selectTask(_ sender: NSButton) { if let key = sender.identifier?.rawValue { openTask(key) } }
    func openTask(_ key: String) {
        selected = key
        destination = TaskRecord.destination(for: key); destinationKey = key
        applyDestination(); openWorkspace(); render()
    }
    func applyDestination() {
        guard let destination, let base, web.url?.host == base.host, !web.isLoading else { return }
        let encoded = String(data: try! JSONSerialization.data(withJSONObject: [destination]), encoding: .utf8)!
        let key = String(data: try! JSONSerialization.data(withJSONObject: [destinationKey ?? ""]), encoding: .utf8)!
        web.evaluateJavaScript("location.hash = \(encoded)[0]; document.dispatchEvent(new CustomEvent('agh:session-select', {detail:{key:\(key)[0]}}))") { _, error in
            if error == nil { self.destination = nil }
        }
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { applyDestination(); render() }
    func webView(_ webView: WKWebView, decidePolicyFor navigationAction: WKNavigationAction, decisionHandler: @escaping (WKNavigationActionPolicy) -> Void) {
        guard let url = navigationAction.request.url else { decisionHandler(.cancel); return }
        if url.absoluteString == "about:blank" || (url.scheme == "http" && url.host == base?.host && url.port == base?.port) {
            decisionHandler(.allow)
        } else {
            if navigationAction.navigationType == .linkActivated && ["https", "http"].contains(url.scheme ?? "") { NSWorkspace.shared.open(url) }
            decisionHandler(.cancel)
        }
    }
    @objc func newTask() {
        openWorkspace()
        // Reuse the workspace's own busy/selection guards and new-conversation operation.
        web.evaluateJavaScript("document.getElementById('new-chat').click(); document.getElementById('chat-toggle').getAttribute('aria-expanded') === 'false' && document.getElementById('chat-toggle').click()")
    }
    @objc func stopSelected() {
        guard let key = selected, let record = records.first(where: { $0.key == key }), record.canStop, !pending.contains(key) else { return }
        pending.insert(key); render()
        // Read the live mutation capability and use the same exact-record control API as Workspace.
        var query = URLComponents(); query.queryItems = [URLQueryItem(name: "key", value: key)]
        get("/api/record?" + query.percentEncodedQuery!) { result in
            guard case .success(let value) = result, let token = value["mutation_token"] as? String, let base = self.base else {
                self.pending.remove(key); self.notice = "Stop unavailable. Refresh the Host record before retrying."; self.render(); return
            }
            var request = URLRequest(url: base.appendingPathComponent("api/record/stop")); request.httpMethod = "POST"
            request.setValue(base.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")), forHTTPHeaderField: "Origin")
            request.setValue(token, forHTTPHeaderField: "X-Host-Action"); request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try? JSONSerialization.data(withJSONObject: ["key": key])
            URLSession.shared.dataTask(with: request) { _, response, error in DispatchQueue.main.async {
                if error != nil || (response as? HTTPURLResponse)?.statusCode != 200 {
                    self.notice = "Stop was not confirmed. Inspect the Host outcome before retrying."; self.pending.remove(key)
                }
                self.refresh(); self.render()
            } }.resume()
        }
    }
    @objc func openInbox() {
        if inboxWindow == nil {
            inboxWindow = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 620, height: 600), styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
            inboxWindow.title = "Agent-Host Inbox"; inboxWindow.isReleasedWhenClosed = false; inboxWindow.center()
            inboxStack = configureStack(in: inboxWindow.contentView!)
        }
        renderInbox(); inboxWindow.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true); popover.performClose(nil)
    }
    func renderInbox() {
        let ids = journal.entries.map { $0.id }
        guard renderedInboxIDs != ids else { return }
        renderedInboxIDs = ids
        clear(inboxStack)
        text("Host execution events · select an event to open its exact record and conversation. Input requests must be answered in that conversation; this app never grants permissions.", in: inboxStack)
        if journal.entries.isEmpty { text("No events yet. Start a task in Workspace.", in: inboxStack) }
        for event in journal.entries {
            button("\(event.at) · \(event.kind) · \(event.title)\n\(event.key)", action: #selector(selectTask(_:)), in: inboxStack, key: event.key)
        }
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        if let key = response.notification.request.content.userInfo["key"] as? String { DispatchQueue.main.async { self.openTask(key) } }
        completionHandler()
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) { completionHandler([.banner, .list]) }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { openWorkspace(); return true }
    func applicationWillTerminate(_ notification: Notification) { timer?.invalidate(); process?.terminate() }
}

let app = NSApplication.shared
let delegate = App()
app.delegate = delegate
app.run()
