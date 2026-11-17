import AppKit
import ApplicationServices
import Carbon.HIToolbox
import Darwin
import MCP
import PersonalCUCore
import ServiceManagement

enum LoginService {
    static var service: SMAppService { .mainApp }
    static var status: String {
        switch service.status {
        case .enabled: return "On"
        case .requiresApproval: return "Waiting for approval in System Settings"
        case .notRegistered: return "Off"
        case .notFound: return "Login item not found in the app"
        @unknown default: return "Unknown"
        }
    }
}

enum RuntimeEndpoint {
    static var directory: URL {
        if let path = ProcessInfo.processInfo.environment["PERSONAL_CU_RUNTIME_DIR"] { return URL(fileURLWithPath: path) }
        return FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support/PersonalCU", isDirectory: true)
    }
    static var path: String { directory.appendingPathComponent("runtime.sock").path }

    static func address() throws -> sockaddr_un {
        let bytes = Array(path.utf8) + [0]
        var address = sockaddr_un()
        guard bytes.count <= MemoryLayout.size(ofValue: address.sun_path) else { throw ArgumentError("Runtime socket path is too long") }
        address.sun_family = sa_family_t(AF_UNIX)
        address.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        withUnsafeMutableBytes(of: &address.sun_path) { destination in destination.copyBytes(from: bytes) }
        return address
    }

    static func connect() throws -> Int32 {
        var address = try address()
        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw ArgumentError("Cannot create local service connection") }
        let result = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard result == 0 else { Darwin.close(fd); throw ArgumentError("PersonalCU service is not running. Open PersonalCU.app first.") }
        var uid: uid_t = 0
        var gid: gid_t = 0
        guard getpeereid(fd, &uid, &gid) == 0, uid == getuid() else { Darwin.close(fd); throw ArgumentError("Service belongs to a different user") }
        return fd
    }

    static func relay() throws {
        signal(SIGPIPE, SIG_IGN)
        let fd = try connect()
        defer { Darwin.close(fd) }
        let group = DispatchGroup()
        group.enter()
        DispatchQueue.global().async {
            defer { Darwin.shutdown(fd, SHUT_WR); group.leave() }
            pump(from: STDIN_FILENO, to: fd)
        }
        pump(from: fd, to: STDOUT_FILENO)
        // The parent owns stdin; do not wait forever when the service exits first.
    }

    private static func pump(from input: Int32, to output: Int32) {
        var buffer = [UInt8](repeating: 0, count: 65536)
        while true {
            let count = Darwin.read(input, &buffer, buffer.count)
            if count < 0 && errno == EINTR { continue }
            guard count > 0 else { return }
            var sent = 0
            while sent < count {
                let written = buffer.withUnsafeBytes { Darwin.write(output, $0.baseAddress!.advanced(by: sent), count - sent) }
                if written < 0 && errno == EINTR { continue }
                guard written > 0 else { return }
                sent += written
            }
        }
    }
}

@MainActor
final class ServiceState {
    static let shared = ServiceState()
    var paused = UserDefaults.standard.bool(forKey: "controlPaused") {
        didSet {
            UserDefaults.standard.set(paused, forKey: "controlPaused")
            if paused { for cancel in inputCancellations.values { cancel() } }
            changed?()
        }
    }
    var backgroundEnabled = UserDefaults.standard.object(forKey: "backgroundEnabled") as? Bool ?? true {
        didSet {
            UserDefaults.standard.set(backgroundEnabled, forKey: "backgroundEnabled")
            if !backgroundEnabled { for cancel in inputCancellations.values { cancel() } }
            changed?()
        }
    }
    var clients = 0
    /// The latest actions clients ran, for the manager window: tool and app, never an argument.
    var recent = ActionLog(capacity: 6)
    var started = Date()
    var socketReady = false
    var changed: (() -> Void)?
    var inputCancellations: [UUID: () -> Void] = [:]
    func record(_ name: String, app: String?) {
        recent.record(name, app: app)
        changed?()
    }
    var facts: ServiceSummary.Facts {
        var facts = ServiceSummary.Facts()
        facts.socketReady = socketReady
        facts.paused = paused
        facts.backgroundEnabled = backgroundEnabled
        facts.accessibility = AXIsProcessTrusted()
        facts.screenRecording = CGPreflightScreenCaptureAccess()
        facts.locked = DesktopSession.locked == true
        return facts
    }
}

@MainActor
final class ResidentService {
    private var listener: Int32 = -1
    private var source: DispatchSourceRead?
    private var tasks: [UUID: Task<Void, Never>] = [:]
    private var clients: [UUID: Int32] = [:]
    private var hosts: [UUID: MCPHost] = [:]
    private var stopping = false

    func start() throws {
        guard !stopping else { throw ArgumentError("PersonalCU service is shutting down") }
        let directory = RuntimeEndpoint.directory
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true, attributes: [.posixPermissions: 0o700])
        var directoryInfo = stat()
        guard lstat(directory.path, &directoryInfo) == 0,
              directoryInfo.st_uid == getuid(), directoryInfo.st_mode & S_IFMT == S_IFDIR,
              directoryInfo.st_mode & 0o077 == 0 else { throw ArgumentError("Runtime directory must be owned by this user with mode 0700") }
        var existing = stat()
        if lstat(RuntimeEndpoint.path, &existing) == 0 {
            guard existing.st_uid == getuid(), existing.st_mode & S_IFMT == S_IFSOCK else { throw ArgumentError("Refusing to replace an unrelated runtime file") }
            if let fd = try? RuntimeEndpoint.connect() { Darwin.close(fd); throw ArgumentError("PersonalCU service is already running") }
            guard unlink(RuntimeEndpoint.path) == 0 else { throw ArgumentError("Cannot remove stale PersonalCU socket") }
        }
        var address = try RuntimeEndpoint.address()
        let fd = Darwin.socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { throw ArgumentError("Cannot create local socket") }
        let bound = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) { Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard bound == 0, chmod(RuntimeEndpoint.path, 0o600) == 0, Darwin.listen(fd, 8) == 0 else {
            Darwin.close(fd); throw ArgumentError("Cannot bind private runtime socket")
        }
        listener = fd
        _ = fcntl(fd, F_SETFL, O_NONBLOCK)
        let source = DispatchSource.makeReadSource(fileDescriptor: fd, queue: .main)
        source.setEventHandler { [weak self] in self?.acceptClients() }
        source.resume()
        self.source = source
        ServiceState.shared.socketReady = true
        ServiceState.shared.changed?()
    }

    private func acceptClients() {
        while !stopping {
            let fd = Darwin.accept(listener, nil, nil)
            guard fd >= 0 else { return }
            var uid: uid_t = 0
            var gid: gid_t = 0
            guard getpeereid(fd, &uid, &gid) == 0, uid == getuid() else { Darwin.close(fd); continue }
            var noSignal: Int32 = 1
            _ = setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &noSignal, socklen_t(MemoryLayout<Int32>.size))
            let id = UUID()
            let host = MCPHost()
            hosts[id] = host
            clients[id] = fd
            ServiceState.shared.clients += 1
            ServiceState.shared.changed?()
            tasks[id] = Task { @MainActor [weak self] in
                defer {
                    Darwin.close(fd)
                    self?.tasks.removeValue(forKey: id)
                    self?.clients.removeValue(forKey: id)
                    self?.hosts.removeValue(forKey: id)
                    ServiceState.shared.clients -= 1
                    ServiceState.shared.changed?()
                }
                do {
                    try await host.run(transport: LocalTransport(input: fd, output: fd))
                } catch {
                    FileHandle.standardError.write(Data(("MCP connection: \(error.localizedDescription)\n").utf8))
                }
            }
        }
    }

    func stop() async {
        stopping = true
        source?.cancel()
        source = nil
        if listener >= 0 { Darwin.close(listener); listener = -1; unlink(RuntimeEndpoint.path) }
        let pendingHosts = Array(hosts.values)
        let pendingTasks = Array(tasks.values)
        // Close every host's admission before the first suspension, including hosts
        // whose connection task has not started yet. No new input may race the drain.
        for host in pendingHosts { host.beginShutdown() }
        for task in pendingTasks { task.cancel() }
        for fd in clients.values { Darwin.shutdown(fd, SHUT_RDWR) }
        ServiceState.shared.socketReady = false
        ServiceState.shared.changed?()
        for host in pendingHosts { await host.shutdown() }
        for task in pendingTasks { await task.value }
    }
}

/// Control-Option-Command-Period pauses input from anywhere, with no window or
/// menu to reach first: Command-Period is the Mac's cancel, and the three
/// modifiers keep it out of every app's own shortcuts. It only pauses; resuming
/// is a deliberate act in the menu or the window. A registered hot key needs no
/// permission and never sees another key. PersonalCU's own keys go to one
/// process, so an action it runs cannot press it.
@MainActor
enum PauseShortcut {
    static let label = "\u{2303}\u{2325}\u{2318}."
    private(set) static var registered = false
    private static var hotKey: EventHotKeyRef?

    static func register() {
        guard !registered else { return }
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let installed = InstallEventHandler(GetApplicationEventTarget(), { _, _, _ in
            // Carbon delivers hot keys on the main thread's event loop.
            MainActor.assumeIsolated { if !ServiceState.shared.paused { ServiceState.shared.paused = true } }
            return noErr
        }, 1, &spec, nil, nil)
        let id = EventHotKeyID(signature: OSType(0x5043_5550), id: 1) // "PCUP"
        registered = installed == noErr && RegisterEventHotKey(UInt32(kVK_ANSI_Period), UInt32(controlKey | optionKey | cmdKey), id,
                                                               GetApplicationEventTarget(), 0, &hotKey) == noErr
    }
}

/// The three macOS permissions, each with the prompt that asks for it and the
/// pane of System Settings that lists it.
private enum Permission: Int, CaseIterable {
    case accessibility, screenRecording, inputMonitoring

    var title: String { ["Accessibility", "Screen Recording", "Input Monitoring"][rawValue] }
    var pane: String { ["Privacy_Accessibility", "Privacy_ScreenCapture", "Privacy_ListenEvent"][rawValue] }
    var granted: Bool {
        switch self {
        case .accessibility: return AXIsProcessTrusted()
        case .screenRecording: return CGPreflightScreenCaptureAccess()
        case .inputMonitoring: return CGPreflightListenEventAccess()
        }
    }
    /// Only debug_tap listens to events, so a service without it is still ready.
    var missing: String { self == .inputMonitoring ? "Not granted (only debug_tap needs it)" : "Not granted" }
    func request() {
        switch self {
        case .accessibility: _ = AXIsProcessTrustedWithOptions([kAXTrustedCheckOptionPrompt.takeUnretainedValue() as String: true] as CFDictionary)
        case .screenRecording: _ = CGRequestScreenCaptureAccess()
        case .inputMonitoring: _ = CGRequestListenEventAccess()
        }
        NSWorkspace.shared.open(URL(string: "x-apple.systempreferences:com.apple.preference.security?\(pane)")!)
    }
}

@MainActor
final class ManagerDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let showOnLaunch: Bool
    private let service = ResidentService()
    private var statusItem: NSStatusItem?
    private var window: NSWindow?
    private let menuStatus = NSMenuItem(title: "", action: nil, keyEquivalent: "")
    private let menuPause = NSMenuItem(title: "Pause input", action: #selector(togglePause), keyEquivalent: "")
    private let menuBackground = NSMenuItem(title: "Allow background control", action: #selector(toggleBackground), keyEquivalent: "")
    private let statusDot = NSImageView()
    private let headline = NSTextField(labelWithString: "")
    private let advice = NSTextField(wrappingLabelWithString: "")
    private let serviceValue = NSTextField(labelWithString: "")
    private let sessionValue = NSTextField(labelWithString: "")
    private var permissionValues: [NSTextField] = []
    private var permissionButtons: [NSButton] = []
    private let pauseButton = NSButton(checkboxWithTitle: "Pause input", target: nil, action: nil)
    private let backgroundButton = NSButton(checkboxWithTitle: "Allow background control", target: nil, action: nil)
    private let loginButton = NSButton(checkboxWithTitle: "Start at login", target: nil, action: nil)
    private let loginNote = NSTextField(labelWithString: "")
    private let pauseNote = NSTextField(labelWithString: "")
    private let recentLabel = NSTextField(wrappingLabelWithString: "")
    private var timer: Timer?
    private var terminationPending = false

    init(showOnLaunch: Bool) { self.showOnLaunch = showOnLaunch; super.init() }

    func applicationDidFinishLaunching(_ notification: Foundation.Notification) {
        NSApp.setActivationPolicy(.accessory)
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        let menu = NSMenu()
        menu.delegate = self
        menuStatus.isEnabled = false
        let open = NSMenuItem(title: "Open PersonalCU", action: #selector(showManager), keyEquivalent: "")
        let quit = NSMenuItem(title: "Quit PersonalCU", action: #selector(quit), keyEquivalent: "q")
        for entry in [menuStatus, .separator(), menuPause, menuBackground, .separator(), open, quit] {
            if entry.action != nil { entry.target = self }
            menu.addItem(entry)
        }
        item.menu = menu
        statusItem = item
        ServiceState.shared.changed = { [weak self] in self?.refresh() }
        do { try service.start() } catch { alert(error.localizedDescription) }
        PauseShortcut.register()
        timer = Timer.scheduledTimer(withTimeInterval: 2, repeats: true) { [weak self] _ in Task { @MainActor in self?.refresh() } }
        refresh()
        if showOnLaunch { showManager() }
    }

    func applicationShouldHandleReopen(_ sender: NSApplication, hasVisibleWindows flag: Bool) -> Bool {
        showManager(); return true
    }

    func menuNeedsUpdate(_ menu: NSMenu) { refresh() }

    @objc func showManager() {
        if window == nil { window = makeWindow() }
        refresh()
        // Shown even when macOS declines to activate an app that another
        // program started in the background.
        window?.orderFrontRegardless()
        window?.makeKeyAndOrderFront(nil)
        NSApp.activate(ignoringOtherApps: true)
    }

    /// The manager lays itself out, so a longer sentence or a larger text size
    /// moves what is below it instead of running under it.
    private func makeWindow() -> NSWindow {
        let panel = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 520, height: 10), styleMask: [.titled, .closable, .miniaturizable], backing: .buffered, defer: false)
        panel.title = "PersonalCU"
        panel.isReleasedWhenClosed = false

        func caption(_ text: String) -> NSTextField {
            let label = NSTextField(labelWithString: text)
            label.textColor = .secondaryLabelColor
            return label
        }
        func heading(_ text: String) -> NSTextField {
            let label = NSTextField(labelWithString: text)
            label.font = .systemFont(ofSize: NSFont.smallSystemFontSize, weight: .semibold)
            label.textColor = .secondaryLabelColor
            return label
        }
        func row(_ views: [NSView], spacing: CGFloat = 8) -> NSStackView {
            let stack = NSStackView(views: views)
            stack.orientation = .horizontal
            stack.alignment = .firstBaseline
            stack.spacing = spacing
            return stack
        }

        headline.font = .systemFont(ofSize: 22, weight: .semibold)
        statusDot.imageScaling = .scaleProportionallyUpOrDown
        statusDot.widthAnchor.constraint(equalToConstant: 14).isActive = true
        statusDot.heightAnchor.constraint(equalToConstant: 14).isActive = true
        let head = row([statusDot, headline], spacing: 10)
        head.alignment = .centerY
        advice.textColor = .secondaryLabelColor
        // Two lines whatever the state says, for the same reason as the list of
        // recent actions below: the window keeps one size.
        advice.maximumNumberOfLines = 2
        advice.stringValue = "\n"
        advice.heightAnchor.constraint(equalToConstant: advice.intrinsicContentSize.height).isActive = true

        var rows: [[NSView]] = [[caption("Service"), serviceValue, NSGridCell.emptyContentView]]
        for permission in Permission.allCases {
            let value = NSTextField(labelWithString: "")
            let button = NSButton(title: "Open Settings\u{2026}", target: self, action: #selector(requestPermission(_:)))
            button.tag = permission.rawValue
            button.controlSize = .small
            button.bezelStyle = .rounded
            permissionValues.append(value)
            permissionButtons.append(button)
            rows.append([caption(permission.title), value, button])
        }
        rows.append([caption("Desktop"), sessionValue, NSGridCell.emptyContentView])
        let grid = NSGridView(views: rows)
        grid.rowSpacing = 8
        grid.columnSpacing = 14
        grid.rowAlignment = .firstBaseline
        grid.column(at: 0).xPlacement = .trailing
        grid.column(at: 2).xPlacement = .trailing

        pauseButton.target = self; pauseButton.action = #selector(togglePause)
        backgroundButton.target = self; backgroundButton.action = #selector(toggleBackground)
        loginButton.target = self; loginButton.action = #selector(toggleLogin)
        for note in [loginNote, pauseNote] {
            note.textColor = .secondaryLabelColor
            note.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        }
        let switches = NSStackView(views: [row([pauseButton, pauseNote]), backgroundButton, row([loginButton, loginNote])])
        switches.orientation = .vertical
        switches.alignment = .leading
        switches.spacing = 8

        recentLabel.font = .monospacedDigitSystemFont(ofSize: NSFont.smallSystemFontSize, weight: .regular)
        // Room for every kept action from the start: a window that grew with the
        // list would change size under a client that is acting in it, and a
        // snapshot of a window that changed size is refused as stale.
        recentLabel.maximumNumberOfLines = ServiceState.shared.recent.capacity
        recentLabel.stringValue = String(repeating: "\n", count: ServiceState.shared.recent.capacity - 1)
        recentLabel.heightAnchor.constraint(equalToConstant: recentLabel.intrinsicContentSize.height).isActive = true
        let footer = NSTextField(wrappingLabelWithString: "The MCP client brings the model. While paused, clients can still read windows. A client looks again to see what an action did.")
        footer.textColor = .tertiaryLabelColor
        footer.font = .systemFont(ofSize: NSFont.smallSystemFontSize)

        func rule() -> NSBox { let box = NSBox(); box.boxType = .separator; return box }
        let content = NSStackView(views: [head, advice, rule(), grid, rule(), heading("CONTROL"), switches, rule(), heading("RECENT ACTIONS"), recentLabel, rule(), footer])
        content.orientation = .vertical
        content.alignment = .leading
        content.spacing = 12
        content.setCustomSpacing(4, after: head)
        content.edgeInsets = NSEdgeInsets(top: 22, left: 28, bottom: 22, right: 28)
        content.translatesAutoresizingMaskIntoConstraints = false
        let root = NSView()
        root.addSubview(content)
        NSLayoutConstraint.activate([
            content.topAnchor.constraint(equalTo: root.topAnchor), content.bottomAnchor.constraint(equalTo: root.bottomAnchor),
            content.leadingAnchor.constraint(equalTo: root.leadingAnchor), content.trailingAnchor.constraint(equalTo: root.trailingAnchor),
            root.widthAnchor.constraint(equalToConstant: 520),
        ])
        for view in content.arrangedSubviews where view is NSBox || view === advice || view === footer || view === recentLabel || view === grid {
            view.widthAnchor.constraint(equalTo: content.widthAnchor, constant: -56).isActive = true
        }
        panel.contentView = root
        panel.center()
        return panel
    }

    private func refresh() {
        let state = ServiceState.shared
        let summary = ServiceSummary(state.facts)
        let (symbol, color): (String?, NSColor) = switch summary.level {
        case .ready: (nil, .systemGreen)
        case .attention: (state.facts.paused && summary.headline == "Paused" ? "pause.fill" : "exclamationmark.triangle.fill", .systemOrange)
        case .stopped: ("exclamationmark.triangle.fill", .systemRed)
        }
        statusItem?.button?.image = MenuBarIcon.image(badge: symbol, description: "PersonalCU: \(summary.headline)")
        statusItem?.button?.toolTip = "PersonalCU: \(summary.headline)"
        let connections = state.clients == 1 ? "1 client connected" : "\(state.clients) clients connected"
        menuStatus.title = "\(summary.headline) \u{00B7} \(connections)"
        // Applysmith's help says "resume control from the menu bar icon", so the item says so while paused.
        menuPause.title = state.paused ? "Resume control" : PauseShortcut.registered ? "Pause input (\(PauseShortcut.label))" : "Pause input"
        menuBackground.state = state.backgroundEnabled ? .on : .off
        guard window != nil else { return }

        statusDot.image = NSImage(systemSymbolName: "circle.fill", accessibilityDescription: nil)
        statusDot.contentTintColor = color
        headline.stringValue = summary.headline
        advice.stringValue = summary.advice.isEmpty ? "Clients can read windows and act in them without raising them." : summary.advice
        serviceValue.stringValue = state.socketReady ? "Running \u{00B7} \(connections)" : "Not running"
        for permission in Permission.allCases {
            let granted = permission.granted
            let value = permissionValues[permission.rawValue]
            value.stringValue = granted ? "Granted" : permission.missing
            value.textColor = granted || permission == .inputMonitoring ? .labelColor : .systemOrange
            permissionButtons[permission.rawValue].isHidden = granted
        }
        sessionValue.stringValue = state.facts.locked ? "Locked" : "Unlocked"
        pauseButton.state = state.paused ? .on : .off
        pauseNote.stringValue = PauseShortcut.registered ? "\(PauseShortcut.label) pauses from anywhere" : "The pause shortcut \(PauseShortcut.label) is taken by another app"
        backgroundButton.state = state.backgroundEnabled ? .on : .off
        loginButton.state = LoginService.service.status == .enabled ? .on : .off
        loginNote.stringValue = switch LoginService.service.status {
        case .requiresApproval: "Waiting for approval in System Settings, Login Items"
        case .notFound: "This copy has no login item; the installed app does"
        default: ""
        }
        let time = DateFormatter()
        time.dateStyle = .none
        time.timeStyle = .medium
        recentLabel.stringValue = state.recent.entries.isEmpty ? "None yet" : state.recent.entries.map { entry in
            "\(time.string(from: entry.at))   \(entry.tool)" + (entry.app.map { " \u{00B7} \($0)" } ?? "")
        }.joined(separator: "\n")
    }

    @objc private func togglePause() { ServiceState.shared.paused.toggle(); refresh() }
    @objc private func toggleBackground() { ServiceState.shared.backgroundEnabled.toggle(); refresh() }
    @objc private func requestPermission(_ sender: NSButton) { Permission(rawValue: sender.tag)?.request() }
    @objc private func toggleLogin() {
        if LoginService.service.status == .enabled {
            Task { do { try await LoginService.service.unregister() } catch { alert(error.localizedDescription) }; refresh() }
        } else {
            do { try LoginService.service.register() } catch { alert(error.localizedDescription) }
            refresh()
        }
    }
    @objc private func quit() { NSApp.terminate(nil) }
    private func alert(_ message: String) { let alert = NSAlert(); alert.messageText = "PersonalCU"; alert.informativeText = message; alert.runModal() }
    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard !terminationPending else { return .terminateLater }
        terminationPending = true
        timer?.invalidate()
        Task { @MainActor in
            // Keep the app/run loop alive until cancelled input has posted its
            // matching releases. A deadline that exits anyway would defeat this.
            await service.stop()
            sender.reply(toApplicationShouldTerminate: true)
        }
        return .terminateLater
    }
    func applicationWillTerminate(_ notification: Foundation.Notification) { timer?.invalidate() }
}
