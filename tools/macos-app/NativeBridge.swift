import AppKit
import WebKit
import CryptoKit
import UniformTypeIdentifiers

// App-only connection selection. The Web surface can request the chooser but
// cannot choose a path, read configuration contents or grant itself access.
final class NativeBridge: NSObject, WKScriptMessageHandler {
    weak var app: App?
    var connecting = false
    var replacingViewer = false
    var pendingDraft: String?
    var visibleTaskKey: String?
    var chatReady = false
    var updates: AppUpdates?

    @objc func checkUpdates() { app?.openPreferences(); updates?.check() }

    func refreshReadiness() {
        app?.get("/api/chat") { result in
            if case .success(let value) = result {
                let original = Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] ?? []
                self.chatReady = value["enabled"] as? Bool == true &&
                    (!(value["agents"] as? [[String: Any]] ?? []).isEmpty || self.arguments(original).contains("--chat-config"))
            } else { self.chatReady = false }
        }
    }
    var support: URL {
        FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("Agent-Host", isDirectory: true)
    }
    var connectionID: String { Bundle.main.object(forInfoDictionaryKey: "AGHJournalID") as? String ?? "default" }
    var connectionFile: URL { support.appendingPathComponent("workspace-connection-\(connectionID).json") }
    var journalID: String {
        let original = Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] ?? []
        let effective = arguments(original)
        if effective == original { return connectionID }
        let data = (try? JSONSerialization.data(withJSONObject: effective)) ?? Data()
        return SHA256.hash(data: data).prefix(8).map { String(format: "%02x", $0) }.joined()
    }
    var draftFile: URL {
        let original = Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] ?? []
        let data = (try? JSONSerialization.data(withJSONObject: arguments(original))) ?? Data()
        let scope = SHA256.hash(data: data).prefix(8).map { String(format: "%02x", $0) }.joined()
        return support.appendingPathComponent("workspace-draft-\(scope).json")
    }

    func install(on app: App) {
        self.app = app
        updates = AppUpdates(bridge: self)
        app.web.configuration.userContentController.add(self, name: "aghConnect")
        app.web.configuration.userContentController.add(self, name: "aghDraft")
        app.web.configuration.userContentController.add(self, name: "aghSelection")
        let selection = """
        (() => { let key = null;
          const update = () => { const route=location.hash.split('?')[0];
            const query=new URLSearchParams(location.hash.split('?')[1] || '');
            window.webkit.messageHandlers.aghSelection.postMessage({key:route === '#executions' ? (query.get('key') || key || '') : ''}); };
          addEventListener('hashchange', update);
          document.addEventListener('agh:session-select', e => {key=e.detail?.key || null; update();});
          addEventListener('DOMContentLoaded', update);
        })();
        """
        app.web.configuration.userContentController.addUserScript(WKUserScript(source: selection, injectionTime: .atDocumentStart, forMainFrameOnly: true))
    }

    func arguments(_ original: [String]) -> [String] {
        guard let data = try? Data(contentsOf: connectionFile),
              let stored = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              stored["original"] as? [String] == original,
              let arguments = stored["arguments"] as? [String] else { return original }
        return arguments
    }

    // MARK: App-owned runtime

    var originalArguments: [String] { Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] ?? [] }
    /// True for a normal build with no imported connection; explicit viewer arguments stay advanced fixtures.
    var usesAppRuntime: Bool { originalArguments.isEmpty && arguments(originalArguments).isEmpty }
    var runtimeRoot: URL { support.appendingPathComponent("runtime", isDirectory: true) }
    var hostBinary: String { Bundle.main.resourceURL!.appendingPathComponent("bin/agent-host").path }

    /// Effective viewer arguments. Explicit arguments or an imported connection are used verbatim;
    /// otherwise the app prepares its own private runtime, offline and within a bounded time.
    func startupArguments(_ original: [String]) throws -> [String] {
        let effective = arguments(original)
        guard original.isEmpty, effective.isEmpty else { return effective }
        let value = try runSync("runtime.py", ["init", "--root", runtimeRoot.path, "--binary", hostBinary], timeout: 40)
        // The helper owns the app runtime's viewer arguments (its own state only, all of its Agents).
        guard value["status"] as? String != "error", let result = value["viewer_arguments"] as? [String], !result.isEmpty else {
            throw NSError(domain: "Agent-Host", code: 2, userInfo: [NSLocalizedDescriptionKey: value["message"] as? String ?? "Local runtime could not be prepared."])
        }
        return result
    }

    func runSync(_ script: String, _ arguments: [String], timeout: TimeInterval) throws -> [String: Any] {
        guard let resources = Bundle.main.resourceURL, let python = Bundle.main.object(forInfoDictionaryKey: "AGHPython") as? String else {
            throw NSError(domain: "Agent-Host", code: 1, userInfo: [NSLocalizedDescriptionKey: "App resources are unavailable."])
        }
        let child = Process(); child.executableURL = URL(fileURLWithPath: python)
        child.arguments = ["-B", resources.appendingPathComponent("tools/macos-app/" + script).path] + arguments
        let output = Pipe(); child.standardOutput = output; child.standardError = FileHandle.nullDevice; child.standardInput = FileHandle.nullDevice
        try child.run()
        let timer = DispatchWorkItem { if child.isRunning { child.terminate() } }
        DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: timer)
        let data = output.fileHandleForReading.readDataToEndOfFile()
        child.waitUntilExit()
        timer.cancel()
        guard child.terminationReason == .exit, data.count <= 1_048_576,
              let value = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw NSError(domain: "Agent-Host", code: 3, userInfo: [NSLocalizedDescriptionKey: "Local runtime preparation failed or timed out."])
        }
        return value
    }

    /// Local Agent settings are the shared Settings page (Models section); there is no second form.
    @objc func setUpLocalAgent() {
        guard let app else { return }
        app.destination = "#settings?section=models"; app.destinationKey = nil
        app.applyDestination(); app.openWorkspace()
    }

    /// Replace only this app's own idle viewer; callers have already quiesced it and saved the draft.
    func relaunchViewer(_ app: App) {
        replacingViewer = true
        let previous = app.process
        previous?.terminationHandler = nil
        previous?.terminate()
        // Reap only our own viewer, outside the UI thread. Its quiesced runtime
        // has no active execution or closeout.
        DispatchQueue.global().async {
            previous?.waitUntilExit()
            DispatchQueue.main.async {
                app.base = nil; app.records = []; app.selected = nil
                self.chatReady = false; self.visibleTaskKey = nil
                app.journalURL = self.support.appendingPathComponent("inbox-\(self.journalID).json")
                app.journal = (try? JSONDecoder().decode(InboxJournal.self, from: Data(contentsOf: app.journalURL))) ?? InboxJournal()
                app.renderedInbox = nil
                app.busy = false; self.replacingViewer = false; self.connecting = false
                do { try app.startViewer() }
                catch { app.notice = "Settings saved. Quit and reopen to retry loading Workspace."; app.render() }
            }
        }
    }

    func userContentController(_ controller: WKUserContentController, didReceive message: WKScriptMessage) {
        guard let app, let base = app.base, message.frameInfo.isMainFrame,
              message.frameInfo.securityOrigin.host == base.host,
              message.frameInfo.securityOrigin.port == base.port,
              message.frameInfo.securityOrigin.protocol == "http" else { return }
        // Readiness always opens the unified Settings; importing a file stays an explicit menu action.
        if message.name == "aghConnect" { setUpLocalAgent() }
        if message.name == "aghSelection", let value = message.body as? [String: Any], let key = value["key"] as? String {
            visibleTaskKey = key.isEmpty ? nil : key
            if !key.isEmpty { app.selected = key; if app.journal.markRead(key) { app.saveJournal() }; app.render() }
        }
        if message.name == "aghDraft", let draft = message.body as? String, draft.utf8.count <= 1_048_576 {
            do { try write(["draft": draft], to: draftFile) }
            catch { app.notice = "Draft backup unavailable. Keep Workspace open and check local storage permissions."; app.render() }
        }
    }

    @objc func connectHost() {
        guard let app, !connecting, app.pending.isEmpty else { return }
        app.openWorkspace()
        let chooser = NSOpenPanel()
        chooser.title = "Import Existing Configuration"
        chooser.message = "Select an existing local runtime's task-handoff.json. Validation does not send a task."
        chooser.allowedContentTypes = [.json]; chooser.canChooseDirectories = false
        chooser.allowsMultipleSelection = false
        chooser.begin { result in
            guard result == .OK, let url = chooser.url else { return }
            self.connecting = true
            self.runJSON("connection.py", [url.path]) { value in
                guard value["status"] as? String == "ready", let config = value["config"] as? String,
                      let home = value["home"] as? String else {
                    self.connecting = false
                    self.alert(value["message"] as? String ?? "Connection validation failed. Check the configuration and retry.")
                    return
                }
                let confirm = NSAlert()
                confirm.messageText = "Use \(value["agent"] as? String ?? "local Agent")'s configuration?"
                confirm.informativeText = "Task state: \(home)\nYour draft is retained. No task will be sent."
                confirm.addButton(withTitle: "Import"); confirm.addButton(withTitle: "Cancel")
                confirm.beginSheetModal(for: app.window) { response in
                    guard response == .alertFirstButtonReturn else { self.connecting = false; return }
                    self.replaceConnection(config: config, home: home)
                }
            }
        }
    }

    func replaceConnection(config: String, home: String) {
        guard let app else { connecting = false; return }
        quiesce(purpose: "connect") { error in
            guard error == nil else { self.connecting = false; self.alert(error!); return }
            self.saveDraft { saved in
                guard saved else { self.connecting = false; self.resume(); return }
                do {
                    let original = Bundle.main.object(forInfoDictionaryKey: "AGHViewerArguments") as? [String] ?? []
                    let current = self.arguments(original)
                    let channels = current.contains("--channel-home")
                    var next: [String] = []
                    guard current.count % 2 == 0 else { throw CocoaError(.fileReadCorruptFile) }
                    for offset in stride(from: 0, to: current.count, by: 2) {
                        let flag = current[offset], value = current[offset + 1]
                        if flag == "--home" || flag == "--chat-config" || (flag == "--channel-home" && value.hasPrefix("cli=")) { continue }
                        next += [flag, value]
                    }
                    next += channels ? ["--channel-home", "cli=" + home] : ["--home", home]
                    next += ["--chat-config", config]
                    try self.write(["original": original, "arguments": next], to: self.connectionFile)
                    self.relaunchViewer(app)
                } catch {
                    self.connecting = false; self.resume(); self.alert("Could not save the connection. Your current workspace is retained.")
                }
            }
        }
    }

    /// `input` goes to the helper's stdin so secrets never appear in an argument list.
    func runJSON(_ script: String, _ arguments: [String], input: Data? = nil, completion: @escaping ([String: Any]) -> Void) {
        guard let resources = Bundle.main.resourceURL,
              let python = Bundle.main.object(forInfoDictionaryKey: "AGHPython") as? String else {
            completion(["status": "error", "message": "App resources are unavailable."]); return
        }
        DispatchQueue.global().async {
            var result: [String: Any] = ["status": "error", "message": "The operation failed. Check local prerequisites and retry."]
            let child = Process(); child.executableURL = URL(fileURLWithPath: python)
            child.arguments = ["-B", resources.appendingPathComponent("tools/macos-app/" + script).path] + arguments
            let output = Pipe(); child.standardOutput = output; child.standardError = FileHandle.nullDevice
            let stdin = Pipe(); child.standardInput = stdin
            do {
                try child.run()
                if let input { stdin.fileHandleForWriting.write(input) }
                try? stdin.fileHandleForWriting.close()
                let data = output.fileHandleForReading.readDataToEndOfFile()
                child.waitUntilExit()
                if data.count <= 1_048_576, let value = try JSONSerialization.jsonObject(with: data) as? [String: Any] { result = value }
            } catch { }
            let delivered = result
            DispatchQueue.main.async { completion(delivered) }
        }
    }

    func post(_ path: String, body: [String: Any], completion: @escaping (String?) -> Void) {
        guard let app, let base = app.base else { completion("Workspace is unavailable. Retry after it reconnects."); return }
        app.get("/api/chat") { result in
            guard case .success(let snapshot) = result, let token = snapshot["mutation_token"] as? String else {
                completion("Workspace activity cannot be verified. Retry later."); return
            }
            var request = URLRequest(url: base.appendingPathComponent(path)); request.httpMethod = "POST"
            request.timeoutInterval = 15
            request.setValue(base.absoluteString.trimmingCharacters(in: CharacterSet(charactersIn: "/")), forHTTPHeaderField: "Origin")
            request.setValue(token, forHTTPHeaderField: "X-Host-Action")
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try? JSONSerialization.data(withJSONObject: body)
            URLSession.shared.dataTask(with: request) { data, response, error in
                let value = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
                let failure = error != nil || (response as? HTTPURLResponse)?.statusCode != 200
                DispatchQueue.main.async { completion(failure ? (value?["error"] as? String ?? "Wait for tasks to finish, then retry.") : nil) }
            }.resume()
        }
    }

    func quiesce(purpose: String, completion: @escaping (String?) -> Void) {
        guard let app, app.pending.isEmpty else { completion("Wait for the pending task control request."); return }
        if purpose == "connect", app.base == nil, app.process?.isRunning != true { completion(nil); return }
        post("api/app/quiesce", body: ["purpose": purpose], completion: completion)
    }
    func resume() {
        post("api/app/resume", body: [:]) { _ in }
        app?.web.evaluateJavaScript("document.documentElement.style.pointerEvents = ''; window.aghMaintenance = false; document.getElementById('draft').disabled = false")
    }

    func saveDraft(notifyFailure: Bool = true, completion: @escaping (Bool) -> Void) {
        guard let app else { completion(false); return }
        app.web.evaluateJavaScript("window.aghSaveDraft ? window.aghSaveDraft() : null") { value, error in
            guard error == nil, let draft = value as? String else {
                if notifyFailure { self.alert("Close Settings and check draft storage before reconnecting.") }
                completion(false); return
            }
            do {
                try self.write(["draft": draft], to: self.draftFile)
                self.pendingDraft = draft
                app.web.evaluateJavaScript("document.documentElement.style.pointerEvents = 'none'; window.aghMaintenance = true; document.activeElement?.blur(); document.getElementById('draft').disabled = true")
                completion(true)
            } catch { if notifyFailure { self.alert("The draft could not be saved. Check local storage permissions.") }; completion(false) }
        }
    }

    func restoreDraft() {
        let data = try? Data(contentsOf: draftFile)
        let value = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
        guard let draft = pendingDraft ?? value?["draft"] as? String else { return }
        let literal = String(data: (try? JSONSerialization.data(withJSONObject: [draft])) ?? Data("[\"\"]".utf8), encoding: .utf8)!
        app?.web.evaluateJavaScript("(() => {const d=document.getElementById('draft'); if (!d) return false; if (!d.value) {d.value=\(literal)[0]; d.dispatchEvent(new Event('input'));} return true;})()") { result, _ in
            if result as? Bool == true { self.pendingDraft = nil }
        }
    }

    func write(_ value: [String: Any], to url: URL) throws {
        try FileManager.default.createDirectory(at: support, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]).write(to: url, options: [.atomic])
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }
    func alert(_ message: String) {
        let alert = NSAlert(); alert.messageText = "Agent-Host"; alert.informativeText = message
        if let window = app?.window { alert.beginSheetModal(for: window) { _ in } }
    }
}
