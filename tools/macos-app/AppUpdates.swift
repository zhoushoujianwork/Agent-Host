import AppKit

/// One controller owns scheduled/manual checks and the private installer handoff.
/// Dev candidates are prepared only by an explicit action in Preferences.
final class AppUpdates: NSObject {
    weak var bridge: NativeBridge?
    var checking = false
    var preparing = false
    var applying = false
    var backgroundOnly = false
    var status = "Not checked yet"
    var lastChecked: Date?
    var target: [String: Any]?
    var prepared: [String: Any]?
    var deferredRevision: String?
    var healthToken: String?
    var healthChecking = false
    var lastIdleAttempt = Date.distantPast
    var deferredUntil: Date?
    var nextCheck = Date.distantPast
    var automaticPaused = false
    var timer: Timer?
    let cache = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
        .appendingPathComponent("Agent-Host/updates", isDirectory: true)
    var app: App? { bridge?.app }
    var metadata: [String: Any] { Bundle.main.object(forInfoDictionaryKey: "AGHBuildMetadata") as? [String: Any] ?? [:] }
    var stable: Bool { metadata["channel"] as? String == "stable" }
    var changed: (() -> Void)?

    init(bridge: NativeBridge) {
        self.bridge = bridge; super.init()
        let defaults = UserDefaults.standard
        lastChecked = defaults.object(forKey: "updates.lastCheck") as? Date
        deferredRevision = defaults.string(forKey: "updates.deferredRevision")
        deferredUntil = defaults.object(forKey: "updates.deferredUntil") as? Date
        nextCheck = defaults.object(forKey: "updates.nextCheck") as? Date ?? .distantPast
        automaticPaused = defaults.bool(forKey: "updates.automaticPaused")
        if automaticPaused { status = "Automatic updates paused after a failure. Choose Check for Updates to retry." }
        readApplyResult()
        let args = CommandLine.arguments
        if let index = args.firstIndex(of: "--agh-update-health"), index + 1 < args.count {
            let token = args[index + 1]
            if (16...64).contains(token.count), token.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber || $0 == "-" || $0 == "_") }) {
                healthToken = token
                do {
                    try prepareCache()
                    try write(["pid": ProcessInfo.processInfo.processIdentifier,
                               "revision": metadata["revision"] as? String ?? "unknown"], name: "started-\(token).json")
                } catch { status = "Update startup could not be confirmed. Previous version is retained." }
            }
        }
        timer = Timer.scheduledTimer(withTimeInterval: 1800, repeats: true) { [weak self] _ in self?.scheduledCheck() }
        DispatchQueue.main.asyncAfter(deadline: .now() + 3) { [weak self] in self?.scheduledCheck() }
    }

    func prepareCache() throws {
        try FileManager.default.createDirectory(at: cache, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        guard cache.resolvingSymlinksInPath() == cache else { throw CocoaError(.fileWriteNoPermission) }
    }
    func write(_ value: [String: Any], name: String) throws {
        let url = cache.appendingPathComponent(name)
        try JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]).write(to: url, options: [.atomic])
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: url.path)
    }
    func refreshUI() {
        app?.renderedPermission = nil
        if app?.preferencesWindow != nil { app?.renderPreferences() }
    }
    func scheduledCheck() {
        readApplyResult()
        guard healthToken == nil, !checking, !preparing, !applying else { return }
        guard !automaticPaused else { refreshUI(); return }
        if Date() < nextCheck { return }
        check()
    }

    @objc func check() {
        guard !checking, !preparing, !applying else { return }
        automaticPaused = false; UserDefaults.standard.set(false, forKey: "updates.automaticPaused")
        checking = true; status = "Checking for updates…"; refreshUI()
        bridge?.runJSON("update.py", ["--bundle", Bundle.main.bundleURL.path, "check"]) { value in
            self.checking = false
            let previousRevision = self.target?["revision"] as? String
            self.target = value["target"] as? [String: Any]
            if previousRevision != self.target?["revision"] as? String { self.prepared = nil }
            let state = value["status"] as? String ?? "error"
            self.status = value["message"] as? String ?? "Check failed. Retry when the connection is available."
            if value["ok"] as? Bool == true {
                self.lastChecked = Date(); UserDefaults.standard.set(self.lastChecked, forKey: "updates.lastCheck")
                self.nextCheck = Date().addingTimeInterval(86400 + Double.random(in: 0...1800))
            } else {
                self.nextCheck = Date().addingTimeInterval(1800)
            }
            UserDefaults.standard.set(self.nextCheck, forKey: "updates.nextCheck")
            if self.stable, let until = self.deferredUntil, until <= Date() {
                self.deferredRevision = nil; self.deferredUntil = nil
                UserDefaults.standard.removeObject(forKey: "updates.deferredRevision")
                UserDefaults.standard.removeObject(forKey: "updates.deferredUntil")
            }
            if state == "available", self.stable, value["automatic_allowed"] as? Bool == true,
               self.target?["revision"] as? String != self.deferredRevision {
                self.prepare(explicit: false)
            }
            self.refreshUI()
        }
    }

    func readApplyResult() {
        let file = cache.appendingPathComponent("last-apply.json")
        guard let attributes = try? FileManager.default.attributesOfItem(atPath: file.path),
              let modified = attributes[.modificationDate] as? Date,
              modified > (UserDefaults.standard.object(forKey: "updates.observedApply") as? Date ?? .distantPast),
              let data = try? Data(contentsOf: file),
              let result = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let ok = result["ok"] as? Bool else { return }
        UserDefaults.standard.set(modified, forKey: "updates.observedApply")
        if !ok {
            status = result["message"] as? String ?? "The previous update failed. The previous app is retained."
            automaticPaused = true
            UserDefaults.standard.set(true, forKey: "updates.automaticPaused")
        }
    }

    @objc func updateNow() {
        if prepared != nil {
            backgroundOnly = false
            deferredRevision = nil; deferredUntil = nil
            UserDefaults.standard.removeObject(forKey: "updates.deferredRevision")
            UserDefaults.standard.removeObject(forKey: "updates.deferredUntil")
            applyWhenIdle(explicitDev: !stable)
        } else { prepare(explicit: true) }
    }
    func prepare(explicit: Bool) {
        guard !checking, !preparing, !applying, let target,
              let revision = target["revision"] as? String, stable || explicit else { return }
        preparing = true; deferredRevision = nil
        backgroundOnly = stable && !explicit
        UserDefaults.standard.removeObject(forKey: "updates.deferredRevision")
        UserDefaults.standard.removeObject(forKey: "updates.deferredUntil"); deferredUntil = nil
        status = "Building the selected version locally…"; refreshUI()
        var args = ["--bundle", Bundle.main.bundleURL.path, "prepare", "--revision", revision]
        if !stable { args.append("--allow-dev") }
        bridge?.runJSON("update.py", args) { value in
            self.preparing = false
            if value["status"] as? String == "prepared" {
                self.prepared = value
                self.status = "Update ready. Waiting for Workspace to be safely idle."
                self.applyWhenIdle(explicitDev: explicit && !self.stable)
            } else { self.status = value["message"] as? String ?? "Build failed. The current app is unchanged." }
            self.refreshUI()
        }
    }

    @objc func later() {
        guard let revision = target?["revision"] as? String else { return }
        deferredRevision = revision
        UserDefaults.standard.set(revision, forKey: "updates.deferredRevision")
        if stable {
            deferredUntil = Date().addingTimeInterval(86400)
            UserDefaults.standard.set(deferredUntil, forKey: "updates.deferredUntil")
        }
        status = stable ? "Deferred for 24 hours. You can choose Update sooner." : "Deferred until you choose Update."
        refreshUI()
    }

    func retryIdle() {
        guard prepared != nil, !applying, deferredRevision == nil, Date().timeIntervalSince(lastIdleAttempt) >= 30 else { return }
        lastIdleAttempt = Date()
        applyWhenIdle(explicitDev: !stable)
    }
    func applyWhenIdle(explicitDev: Bool) {
        guard app != nil, let bridge, let prepared, !applying, !bridge.connecting,
              var candidate = prepared["candidate"] as? [String: Any],
              let manifest = prepared["cache_manifest"] as? [String: Any],
              let manifestPath = manifest["path"] as? String, let digest = manifest["digest"] as? String else { return }
        if backgroundOnly && NSApp.isActive {
            status = "Update ready. It will install when the app is in the background."; refreshUI(); return
        }
        applying = true
        bridge.quiesce(purpose: "update") { error in
            if let error { self.applying = false; self.status = "Waiting: " + error; self.refreshUI(); return }
            bridge.saveDraft(notifyFailure: false) { saved in
                guard saved else {
                    self.applying = false; self.status = "Waiting: close Settings and check draft storage before updating."
                    bridge.resume(); self.refreshUI(); return
                }
                do {
                    try self.prepareCache()
                    let token = UUID().uuidString
                    candidate["manifest_path"] = manifestPath; candidate["manifest_digest"] = digest
                    let handoff: [String: Any] = ["schema": 1, "install_bundle": Bundle.main.bundleURL.path,
                        "candidate": candidate, "owner_pid": ProcessInfo.processInfo.processIdentifier,
                        "timestamp": Date().timeIntervalSince1970, "health_token": token, "allow_dev": explicitDev]
                    let file = "handoff-\(token).json"
                    try self.write(handoff, name: file)
                    guard let resources = Bundle.main.resourceURL,
                          let python = Bundle.main.object(forInfoDictionaryKey: "AGHPython") as? String else { throw CocoaError(.fileReadNoSuchFile) }
                    // Copy the helper out of the bundle before replacement.
                    let helper = self.cache.appendingPathComponent("installer-\(token).py")
                    try FileManager.default.copyItem(at: resources.appendingPathComponent("tools/macos-app/update.py"), to: helper)
                    try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: helper.path)
                    let child = Process(); child.executableURL = URL(fileURLWithPath: python)
                    child.arguments = ["-B", helper.path, "--bundle", Bundle.main.bundleURL.path, "apply", "--handoff", self.cache.appendingPathComponent(file).path]
                    let log = self.cache.appendingPathComponent("last-apply.json")
                    FileManager.default.createFile(atPath: log.path, contents: nil, attributes: [.posixPermissions: 0o600])
                    child.standardOutput = try FileHandle(forWritingTo: log); child.standardError = FileHandle.nullDevice
                    self.status = "Installing the verified update…"; self.refreshUI()
                    try child.run()
                    // The installer validates the pinned candidate and writes
                    // readiness before asking this instance to exit.
                    let ready = self.cache.appendingPathComponent("ready-\(token).json")
                    DispatchQueue.global().async {
                        let deadline = Date().addingTimeInterval(25)
                        var accepted = false
                        while child.isRunning && Date() < deadline {
                            if let data = try? Data(contentsOf: ready),
                               let result = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                               result["pid"] as? Int == Int(child.processIdentifier),
                               result["owner_pid"] as? Int == Int(ProcessInfo.processInfo.processIdentifier),
                               result["revision"] as? String == candidate["revision"] as? String {
                                accepted = true; break
                            }
                            Thread.sleep(forTimeInterval: 0.2)
                        }
                        let canExit = accepted
                        if !canExit && child.isRunning { child.terminate() }
                        DispatchQueue.main.async {
                            if canExit {
                                UserDefaults.standard.set(!self.backgroundOnly, forKey: "updates.showWorkspaceOnRelaunch")
                                NSApp.terminate(nil)
                            }
                            else {
                                self.applying = false; self.prepared = nil
                                self.status = "Installer validation failed. Your current app remains open. Check for updates to retry."
                                bridge.resume(); self.refreshUI()
                            }
                        }
                    }
                } catch {
                    self.applying = false; self.status = "Could not start the installer. Your current app is retained."
                    bridge.resume(); self.refreshUI()
                }
            }
        }
    }

    func reportHealth() {
        guard let token = healthToken, let app, let base = app.base,
              !app.web.isLoading, app.web.url?.host == base.host, !healthChecking else { return }
        healthChecking = true
        app.web.evaluateJavaScript("document.readyState === 'complete' && !!document.getElementById('draft') && typeof window.aghSaveDraft === 'function'") { ready, error in
            guard error == nil, ready as? Bool == true else { self.healthChecking = false; return }
            self.confirmViewerHealth(token: token, app: app)
        }
    }

    func confirmViewerHealth(token: String, app: App) {
        app.get("/api/chat") { chat in
            guard case .success = chat else { self.healthChecking = false; return }
            app.get("/api/records") { records in
                self.healthChecking = false
                guard case .success = records else { return }
                do {
                    try self.write(["pid": ProcessInfo.processInfo.processIdentifier,
                                    "revision": self.metadata["revision"] as? String ?? "unknown"], name: "health-\(token).json")
                    self.healthToken = nil
                } catch { self.status = "Update health could not be confirmed."; self.refreshUI() }
            }
        }
    }

    func render(in stack: NSStackView) {
        guard let app else { return }
        let version = metadata["version"] as? String ?? "Development preview"
        let channel = metadata["channel"] as? String ?? "dev"
        app.text("Updates · \(version) · \(channel)", in: stack)
        app.text(status, in: stack)
        if let lastChecked { app.text("Last checked: \(DateFormatter.localizedString(from: lastChecked, dateStyle: .short, timeStyle: .short))", in: stack) }
        let check = NSButton(title: "Check for Updates", target: self, action: #selector(check))
        check.isEnabled = !checking && !preparing && !applying; stack.addArrangedSubview(check)
        if target != nil, !preparing && !applying {
            let update = NSButton(title: "Update", target: self, action: #selector(updateNow))
            stack.addArrangedSubview(update)
            stack.addArrangedSubview(NSButton(title: "Later", target: self, action: #selector(later)))
        }
    }
}
