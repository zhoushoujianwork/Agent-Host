import AppKit
import WebKit
import UserNotifications

final class App: NSObject, NSApplicationDelegate, NSMenuItemValidation, UNUserNotificationCenterDelegate, WKNavigationDelegate {
    let status = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    let popover = NSPopover()
    let web = WKWebView()
    let native = NativeBridge()
    var window: NSWindow!
    var inboxWindow: NSWindow!
    var inboxStack = NSStackView()
    var renderedInbox: String?
    var inboxAttentionOnly = false
    var inboxQuery = ""
    var inboxFilter: NSSegmentedControl!
    var preferencesWindow: NSWindow!
    var preferencesStack = NSStackView()
    var renderedPermission: String?
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
    let launchDate = Date()
    var destination: String?
    var destinationKey: String?
    let center = UNUserNotificationCenter.current()
    var completions: Bool {
        get { UserDefaults.standard.bool(forKey: "completionNotifications") }
        set { UserDefaults.standard.set(newValue, forKey: "completionNotifications") }
    }

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.regular)
        installMenus()
        window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1200, height: 800),
                          styleMask: [.titled, .closable, .miniaturizable, .resizable], backing: .buffered, defer: false)
        window.title = "Agent-Host Workspace"; window.contentView = web; window.isReleasedWhenClosed = false
        window.center(); web.navigationDelegate = self
        native.install(on: self)
        web.loadHTMLString("<h1>Agent-Host</h1><p>Loading local workspace. Use the AGH menu bar entry for startup diagnostics.</p>", baseURL: nil)
        let controller = NSViewController(); controller.view = NSView(frame: NSRect(x: 0, y: 0, width: 360, height: 400))
        stack = configureStack(in: controller.view)
        popover.contentViewController = controller; popover.contentSize = NSSize(width: 360, height: 400); popover.behavior = .transient; popover.animates = false
        status.button?.target = self; status.button?.action = #selector(togglePopover)
        center.delegate = self
        let category = UNNotificationCategory(identifier: "TASK", actions: [UNNotificationAction(identifier: "OPEN", title: "Open task", options: .foreground)], intentIdentifiers: [])
        center.setNotificationCategories([category])
        do {
            let support = try FileManager.default.url(for: .applicationSupportDirectory, in: .userDomainMask, appropriateFor: nil, create: true)
                .appendingPathComponent("Agent-Host", isDirectory: true)
            try FileManager.default.createDirectory(at: support, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
            // Each configured source set has its own journal, including across rebuilds.
            let name = native.journalID
            journalURL = support.appendingPathComponent("inbox-\(name).json")
            if FileManager.default.fileExists(atPath: journalURL.path) {
                journal = try JSONDecoder().decode(InboxJournal.self, from: Data(contentsOf: journalURL))
            }
            try startViewer()
        } catch { notice = "Startup failed: \(error.localizedDescription). Rebuild the app with valid viewer configuration." }
        render()
        if native.updates?.healthToken == nil || UserDefaults.standard.bool(forKey: "updates.showWorkspaceOnRelaunch") { openWorkspace() }
        center.requestAuthorization(options: [.alert, .badge]) { _, _ in self.refreshPermission() }
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { _ in self.refresh() }
    }

    func menuItem(_ title: String, _ action: Selector?, _ key: String = "", _ modifiers: NSEvent.ModifierFlags = .command, target: AnyObject? = nil) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: action, keyEquivalent: key)
        item.keyEquivalentModifierMask = modifiers; item.target = target
        return item
    }
    func submenu(_ title: String, _ items: [NSMenuItem]) -> NSMenuItem {
        let parent = NSMenuItem(); let menu = NSMenu(title: title)
        items.forEach { menu.addItem($0) }; parent.submenu = menu
        return parent
    }
    /// Menu items reuse the popover's actions; Edit items go to the first responder so WebKit handles them.
    func installMenus() {
        let main = NSMenu()
        main.addItem(submenu("Agent-Host", [
            menuItem("App Preferences…", #selector(openPreferences), target: self),
            menuItem("Local Agent Settings…", #selector(NativeBridge.setUpLocalAgent), target: native),
            menuItem("Import Existing Configuration…", #selector(NativeBridge.connectHost), target: native),
            menuItem("Check for Updates…", #selector(NativeBridge.checkUpdates), target: native),
            .separator(),
            menuItem("Quit Agent-Host", #selector(NSApplication.terminate(_:)), "q")]))
        main.addItem(submenu("File", [
            menuItem("New Task", #selector(newTask), "n", target: self),
            menuItem("Settings", #selector(openSettings), ",", target: self)]))
        main.addItem(submenu("Edit", [
            menuItem("Undo", Selector(("undo:")), "z"),
            menuItem("Redo", Selector(("redo:")), "z", [.command, .shift]),
            .separator(),
            menuItem("Cut", #selector(NSText.cut(_:)), "x"),
            menuItem("Copy", #selector(NSText.copy(_:)), "c"),
            menuItem("Paste", #selector(NSText.paste(_:)), "v"),
            menuItem("Select All", #selector(NSText.selectAll(_:)), "a")]))
        let windowMenu = submenu("Window", [
            menuItem("Workspace", #selector(openWorkspace), "1", target: self),
            menuItem("Inbox", #selector(openInbox), "2", target: self),
            menuItem("Show Status", #selector(showStatus), "m", [.command, .shift], target: self),
            .separator(),
            menuItem("Minimize", #selector(NSWindow.performMiniaturize(_:)), "m")])
        main.addItem(windowMenu)
        NSApp.mainMenu = main; NSApp.windowsMenu = windowMenu.submenu
    }
    /// New Task drives the loaded workspace page, so it is unavailable until that page is live.
    var workspaceReady: Bool { base != nil && web.url?.host == base?.host && !web.isLoading && native.chatReady }
    func validateMenuItem(_ item: NSMenuItem) -> Bool {
        if item.action == #selector(newTask) { return workspaceReady }
        return true
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
    @discardableResult func button(_ title: String, action: Selector, in target: NSStackView, key: String? = nil, enabled: Bool = true, label: String? = nil) -> NSButton {
        let button = NSButton(title: title, target: self, action: action); button.isEnabled = enabled
        button.setAccessibilityLabel(label ?? title)
        if let key { button.identifier = NSUserInterfaceItemIdentifier(key) }
        target.addArrangedSubview(button)
        return button
    }
    func clear(_ target: NSStackView) { target.arrangedSubviews.forEach { target.removeArrangedSubview($0); $0.removeFromSuperview() } }

    func startViewer() throws {
        guard let resources = Bundle.main.resourceURL,
              let python = Bundle.main.object(forInfoDictionaryKey: "AGHPython") as? String,
              let configuredArguments = Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] else {
            throw NSError(domain: "Agent-Host", code: 1, userInfo: [NSLocalizedDescriptionKey: "Missing app configuration"])
        }
        let arguments = try native.startupArguments(configuredArguments)
        let child = Process(); child.executableURL = URL(fileURLWithPath: python)
        child.arguments = ["-B", resources.appendingPathComponent("tools/run-viewer/server.py").path] + arguments + ["--port", "0", "--no-reports", "--host-binary", resources.appendingPathComponent("bin/agent-host").path]
        child.currentDirectoryURL = resources
        // Finder does not inherit a shell's PATH. Keep standard local command
        // locations available to the bundled Host and Offload runtime.
        var environment = ProcessInfo.processInfo.environment
        let standardPaths = ["/opt/homebrew/bin", "/usr/local/bin", NSHomeDirectory() + "/.local/bin", "/usr/bin", "/bin"]
        environment["PATH"] = (standardPaths + (environment["PATH"] ?? "").components(separatedBy: ":")).filter { !$0.isEmpty }.joined(separator: ":")
        child.environment = environment
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
            DispatchQueue.main.async {
                guard self.base == base else { return }
                completion(result)
            }
        }.resume()
    }
    func refresh() {
        guard base != nil, !busy else { return }; busy = true
        native.refreshReadiness()
        native.updates?.reportHealth()
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
            // Populate historical inbox without flooding macOS: delivery needs a parsed time since launch.
            if saveJournal() { fresh.forEach(notify) }
            pending = pending.filter { key in records.contains { $0.key == key && $0.active } }
            native.updates?.retryIdle()
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
    @discardableResult func saveJournal() -> Bool {
        do {
            try JSONEncoder().encode(journal).write(to: journalURL, options: [.atomic])
            try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: journalURL.path)
            return true
        } catch {
            notice = "Inbox could not be saved: \(error.localizedDescription). Check Application Support permissions."
            return false
        }
    }
    func refreshPermission() {
        center.getNotificationSettings { settings in DispatchQueue.main.async {
            self.permission = settings.authorizationStatus == .denied ? "Notifications denied. Enable Agent-Host in System Settings → Notifications. Inbox remains available." : "Notifications follow macOS preferences. Inbox is always available."
            self.render()
        } }
    }
    /// The task the user is looking at: selected while the Workspace window is visible and key.
    var viewedKey: String? { window.isVisible && window.isKeyWindow ? native.visibleTaskKey : nil }
    func notify(_ event: InboxEvent) {
        guard event.shouldDeliver(completions: completions, since: launchDate, viewing: viewedKey) else { return }
        let content = UNMutableNotificationContent(); content.title = "Agent-Host · \(event.label)"
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
        let attention = journal.attentionCount
        let state = !notice.isEmpty ? "Unavailable" : attention > 0 ? "Attention needed" : records.contains(where: { $0.active }) ? "Running" : "Idle"
        let summary = attention > 0 ? "\(state) · \(attention) task\(attention == 1 ? "" : "s")" : state
        status.button?.title = "AGH · \(state)"; status.button?.setAccessibilityLabel("Agent-Host: \(summary)")
        clear(stack)
        text("Agent-Host · \(summary)", in: stack)
        if !notice.isEmpty { text(notice, in: stack) }
        let record = records.first(where: { $0.key == selected })
        if let record {
            text("\(record.title.prefix(80))\n\(record.agent) · \(record.progress.prefix(80))", in: stack)
            button("Open task", action: #selector(selectTask(_:)), in: stack, key: record.key)
        } else {
            text(selected == nil ? "No tasks yet." : "Selected task unavailable. Open Workspace to inspect its source.", in: stack)
        }
        button("Open Workspace", action: #selector(openWorkspace), in: stack)
        if let record, record.active {
            button(pending.contains(record.key) ? "Stop requested · waiting for Host" : "Stop", action: #selector(stopSelected), in: stack,
                   enabled: notice.isEmpty && record.canStop && !pending.contains(record.key))
            if !record.canStop { text("Stop is unavailable here. Use the original Host entry.", in: stack) }
        }
        for other in records.filter({ $0.active && $0.key != selected }).prefix(2) {
            button("Select: \(other.title.prefix(40)) · \(other.agent)", action: #selector(chooseActive(_:)), in: stack, key: other.key)
        }
        button("New Task", action: #selector(newTask), in: stack, enabled: workspaceReady)
        let unread = journal.unreadCount
        button(unread > 0 ? "Inbox (\(unread) unread)" : "Inbox", action: #selector(openInbox), in: stack)
        if let focusTitle, let replacement = stack.arrangedSubviews.compactMap({ $0 as? NSButton }).first(where: { $0.title == focusTitle && $0.identifier == focusID }) {
            replacement.window?.makeFirstResponder(replacement)
        }
        if inboxWindow != nil { renderInbox() }
        if preferencesWindow != nil { renderPreferences() }
    }
    @objc func openSettings() {
        destination = "#settings"; destinationKey = nil
        applyDestination(); openWorkspace()
    }
    @objc func showStatus() {
        refreshPermission()
        guard !popover.isShown, let button = status.button else { return }
        popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY)
    }
    @objc func openPreferences() {
        if preferencesWindow == nil {
            preferencesWindow = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 460, height: 260), styleMask: [.titled, .closable], backing: .buffered, defer: false)
            preferencesWindow.title = "Agent-Host Preferences"; preferencesWindow.isReleasedWhenClosed = false; preferencesWindow.center()
            preferencesStack = configureStack(in: preferencesWindow.contentView!)
        }
        refreshPermission(); renderPreferences()
        preferencesWindow.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true); popover.performClose(nil)
    }
    func renderPreferences() {
        let signature = "\(permission)|\(completions)|\(native.updates?.status ?? "")"
        guard renderedPermission != signature else { return }
        renderedPermission = signature
        clear(preferencesStack)
        let setup = NSButton(title: "Local Agent Settings…", target: native, action: #selector(NativeBridge.setUpLocalAgent))
        preferencesStack.addArrangedSubview(setup)
        let connect = NSButton(title: "Import existing configuration…", target: native, action: #selector(NativeBridge.connectHost))
        preferencesStack.addArrangedSubview(connect)
        text("Failed and input-needed tasks send a quiet notification. Unknown outcomes appear in Inbox only. Started and stopped events never notify. A task you are viewing in Workspace is not notified.", in: preferencesStack)
        let box = NSButton(checkboxWithTitle: "Notify when a task completes", target: self, action: #selector(toggleCompletions))
        box.state = completions ? .on : .off; box.setAccessibilityLabel("Notify when a task completes")
        preferencesStack.addArrangedSubview(box)
        text(permission, in: preferencesStack)
        native.updates?.render(in: preferencesStack)
    }
    @objc func toggleCompletions() { completions.toggle(); renderedPermission = nil; render() }
    @objc func togglePopover() {
        refreshPermission()
        if popover.isShown { popover.performClose(nil) }
        else if let button = status.button { popover.show(relativeTo: button.bounds, of: button, preferredEdge: .minY) }
    }
    @objc func openWorkspace() {
        if window.isMiniaturized { window.deminiaturize(nil) }
        window.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true); popover.performClose(nil)
    }
    @objc func chooseActive(_ sender: NSButton) { selected = sender.identifier?.rawValue; render() }
    @objc func selectTask(_ sender: NSButton) { if let key = sender.identifier?.rawValue { openTask(key) } }
    func openTask(_ key: String) {
        selected = key
        if journal.markRead(key) { saveJournal() }
        destination = TaskRecord.destination(for: key); destinationKey = key
        applyDestination(); openWorkspace(); render()
    }
    func applyDestination() {
        guard let destination, let base, web.url?.host == base.host, !web.isLoading else { return }
        let encoded = String(data: try! JSONSerialization.data(withJSONObject: [destination]), encoding: .utf8)!
        let key = String(data: try! JSONSerialization.data(withJSONObject: [destinationKey ?? ""]), encoding: .utf8)!
        let select = destinationKey == nil ? "" : "; document.dispatchEvent(new CustomEvent('agh:session-select', {detail:{key:\(key)[0]}}))"
        web.evaluateJavaScript("location.hash = \(encoded)[0]" + select) { _, error in
            if error == nil { self.destination = nil }
        }
    }
    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) { native.restoreDraft(); applyDestination(); render() }
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
        guard workspaceReady else { native.alert(native.usesAppRuntime ? "Open Local Agent Settings (Agent-Host menu) and check the local Agent configuration before starting a new task." : "Connect a configured Host before starting a new task."); return }
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
            // The filter and search field live outside the rebuilt list so typing keeps focus.
            let content = inboxWindow.contentView!
            inboxFilter = NSSegmentedControl(labels: ["All", "Needs attention"], trackingMode: .selectOne, target: self, action: #selector(inboxFilterChanged))
            inboxFilter.selectedSegment = inboxAttentionOnly ? 1 : 0
            inboxFilter.setAccessibilityLabel("Inbox filter")
            let search = NSSearchField(); search.placeholderString = "Search tasks"; search.stringValue = inboxQuery
            search.sendsSearchStringImmediately = true; search.target = self; search.action = #selector(inboxSearchChanged(_:))
            search.setAccessibilityLabel("Search inbox tasks")
            let header = NSStackView(views: [inboxFilter, search]); header.orientation = .horizontal; header.spacing = 8
            header.translatesAutoresizingMaskIntoConstraints = false
            let list = NSView(); list.translatesAutoresizingMaskIntoConstraints = false
            content.addSubview(header); content.addSubview(list)
            NSLayoutConstraint.activate([
                header.topAnchor.constraint(equalTo: content.topAnchor, constant: 12),
                header.leadingAnchor.constraint(equalTo: content.leadingAnchor, constant: 16),
                header.trailingAnchor.constraint(equalTo: content.trailingAnchor, constant: -16),
                list.topAnchor.constraint(equalTo: header.bottomAnchor, constant: 8),
                list.leadingAnchor.constraint(equalTo: content.leadingAnchor), list.trailingAnchor.constraint(equalTo: content.trailingAnchor),
                list.bottomAnchor.constraint(equalTo: content.bottomAnchor)])
            content.layoutSubtreeIfNeeded()
            inboxStack = configureStack(in: list)
        }
        renderInbox(); inboxWindow.makeKeyAndOrderFront(nil); NSApp.activate(ignoringOtherApps: true); popover.performClose(nil)
    }
    @objc func inboxFilterChanged() { inboxAttentionOnly = inboxFilter.selectedSegment == 1; renderInbox() }
    @objc func inboxSearchChanged(_ sender: NSSearchField) { inboxQuery = sender.stringValue; renderInbox() }
    func relative(_ event: InboxEvent) -> String {
        guard let date = event.date else { return event.at }
        return date > Date() ? "just now" : RelativeDateTimeFormatter().localizedString(for: date, relativeTo: Date())
    }
    func renderInbox() {
        let tasks = journal.tasks(attentionOnly: inboxAttentionOnly, query: inboxQuery)
        // Relative times change by the minute; everything else comes from journal state.
        let signature = ([inboxAttentionOnly ? "A" : "*", inboxQuery, String(Int(Date().timeIntervalSince1970 / 60))]
            + tasks.map { "\($0.key)|\($0.latest.id)|\($0.eventCount)|\($0.unread)|\($0.needsAttention)" }).joined(separator: "\n")
        guard renderedInbox != signature else { return }
        renderedInbox = signature
        clear(inboxStack)
        text("Host execution events grouped by task. Open shows the exact record and conversation. Acknowledge clears attention only; this app never grants permissions.", in: inboxStack)
        if tasks.isEmpty {
            text(journal.entries.isEmpty ? "No events yet. Start a task in Workspace." : inboxAttentionOnly && inboxQuery.isEmpty ? "No tasks need attention." : "No matching tasks.", in: inboxStack)
        }
        for task in tasks {
            let row = NSStackView(); row.orientation = .horizontal; row.spacing = 8
            let title = "\(task.unread ? "● " : "")\(task.latest.title.prefix(80))\n\(task.latest.label) · \(relative(task.latest))"
            let label = "\(task.unread ? "Unread. " : "")\(task.latest.label). \(task.latest.title.prefix(80)). \(relative(task.latest))"
            let open = button(title, action: #selector(openInboxTask(_:)), in: row, key: task.key, label: label)
            let record = records.first { $0.key == task.key }
            open.toolTip = "\(record?.agent ?? "Unknown Agent") · \(task.key.components(separatedBy: ":").first ?? "Host")\n\(task.latest.at)"
            if task.needsAttention { button("Acknowledge", action: #selector(acknowledgeInboxTask(_:)), in: row, key: task.key, label: "Acknowledge \(task.latest.title.prefix(80))") }
            inboxStack.addArrangedSubview(row)
        }
    }
    @objc func openInboxTask(_ sender: NSButton) { if let key = sender.identifier?.rawValue { openTask(key) } }
    @objc func acknowledgeInboxTask(_ sender: NSButton) {
        guard let key = sender.identifier?.rawValue, journal.acknowledge(key) else { return }
        saveJournal(); render()
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, didReceive response: UNNotificationResponse, withCompletionHandler completionHandler: @escaping () -> Void) {
        if let key = response.notification.request.content.userInfo["key"] as? String { DispatchQueue.main.async { self.openTask(key) } }
        completionHandler()
    }
    func userNotificationCenter(_ center: UNUserNotificationCenter, willPresent notification: UNNotification, withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void) {
        // Do not alert for the task the user is already viewing in Workspace.
        let key = notification.request.content.userInfo["key"] as? String
        completionHandler(key != nil && key == viewedKey ? [] : [.banner, .list])
    }
    func applicationShouldTerminateAfterLastWindowClosed(_ sender: NSApplication) -> Bool { false }
    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool { openWorkspace(); return true }
    func applicationWillTerminate(_ notification: Notification) { timer?.invalidate(); process?.terminate() }
}

let app = NSApplication.shared
let delegate = App()
app.delegate = delegate
app.run()
