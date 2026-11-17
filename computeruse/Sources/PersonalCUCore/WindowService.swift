import AppKit
import ApplicationServices

@MainActor
public enum WindowService {
    public struct Window {
        public let id: UInt32
        public let pid: pid_t
        public let title: String
        public let rectangle: CGRect
        public let onScreen: Bool
        public let layer: Int
        public var json: [String: Any] {
            ["window_id": Int(id), "pid": Int(pid), "title": title, "rect": rectangleData(rectangle), "on_screen": onScreen, "layer": layer]
        }
    }

    public static func windows(pid: pid_t? = nil, onlyOnScreen: Bool = false) -> [Window] {
        // Only optionOnScreenOnly promises front-to-back ordering. The default
        // remains the complete inventory, including off-screen/minimized windows.
        let options: CGWindowListOption = onlyOnScreen ? [.optionOnScreenOnly, .excludeDesktopElements] : [.optionAll, .excludeDesktopElements]
        let rows = CGWindowListCopyWindowInfo(options, kCGNullWindowID) as? [[String: Any]] ?? []
        return rows.compactMap { parse($0, pid: pid) }
    }

    /// One window by id, asking the window server for that window alone. That
    /// query answers only for a window on screen, so one that is on another
    /// Space, behind a full-screen app or minimized is found in the whole
    /// inventory instead: it is still the same window, not a changed one.
    public static func window(id: UInt32, pid: pid_t) -> Window? {
        let rows = CGWindowListCopyWindowInfo([.optionIncludingWindow, .excludeDesktopElements], id) as? [[String: Any]] ?? []
        return rows.lazy.compactMap { parse($0, pid: pid) }.first { $0.id == id } ?? windows(pid: pid).first { $0.id == id }
    }

    private static func parse(_ row: [String: Any], pid: pid_t?) -> Window? {
        guard let owner = row[kCGWindowOwnerPID as String] as? Int,
              owner > 0, pid == nil || Int(pid!) == owner,
              let id = row[kCGWindowNumber as String] as? UInt32,
              let dictionary = row[kCGWindowBounds as String] as? [String: Any],
              let rectangle = CGRect(dictionaryRepresentation: dictionary as CFDictionary),
              rectangle.width > 1, rectangle.height > 1 else { return nil }
        return Window(id: id, pid: pid_t(owner), title: row[kCGWindowName as String] as? String ?? "", rectangle: rectangle,
                      onScreen: row[kCGWindowIsOnscreen as String] as? Bool ?? false,
                      layer: row[kCGWindowLayer as String] as? Int ?? 0)
    }

    public static func resolvePID(_ args: [String: Any], optional: Bool = false) throws -> pid_t? {
        guard args["pid"] == nil || args["bundle_id"] == nil else { throw LocalCUError.invalidArgument("Use pid OR bundle_id") }
        if let number = args["pid"] as? NSNumber {
            guard CFGetTypeID(number) != CFBooleanGetTypeID(), number.int64Value > 0,
                  number.doubleValue == Double(number.int64Value), number.int64Value <= Int32.max,
                  NSRunningApplication(processIdentifier: number.int32Value)?.isTerminated == false else { throw LocalCUError.target("Invalid or exited pid") }
            return number.int32Value
        }
        if let bundle = args["bundle_id"] as? String {
            let apps = NSRunningApplication.runningApplications(withBundleIdentifier: bundle).filter { !$0.isTerminated }
            guard apps.count == 1 else { throw LocalCUError.target("Bundle must resolve to one running application") }
            return apps[0].processIdentifier
        }
        if optional { return nil }
        throw LocalCUError.invalidArgument("Supply pid or bundle_id")
    }

    public static func window(_ args: [String: Any]) throws -> Window {
        guard let id = args["window_id"] as? NSNumber, CFGetTypeID(id) != CFBooleanGetTypeID(),
              id.doubleValue == Double(id.uint32Value), id.uint32Value > 0 else { throw LocalCUError.invalidArgument("window_id must be a positive UInt32") }
        let pid = try resolvePID(args, optional: true)
        guard let found = windows(pid: pid).first(where: { $0.id == id.uint32Value }) else { throw LocalCUError.target("Window does not exist or belongs to another target") }
        return found
    }

    public static func axWindow(_ window: Window) throws -> (element: AXUIElement, index: Int) {
        try AXReader.checkPermission()
        let root = AXUIElementCreateApplication(window.pid)
        _ = AXUIElementSetMessagingTimeout(root, 5)
        let entries = try AXReader.elements(root, kAXWindowsAttribute)
        let candidates = try entries.enumerated().filter { _, element in
            guard try AXReader.string(element, kAXRoleAttribute) == kAXWindowRole else { return false }
            guard let rect = try AXReader.rectangle(element) else { return false }
            return sameRectangle(rect, window.rectangle, tolerance: 2)
        }
        let titled = try candidates.filter { try AXReader.string($0.element, kAXTitleAttribute) == window.title }
        let matches = titled.isEmpty ? candidates : titled
        guard matches.count == 1, let matched = matches.first else { throw LocalCUError.target("Window cannot be bound unambiguously to AX state") }
        return (matched.element, matched.offset)
    }

    public static func stateArguments(_ args: [String: Any]) throws -> [String: Any] {
        var result = args
        if args["window_id"] != nil {
            let selected = try window(args)
            result["pid"] = Int(selected.pid)
            result.removeValue(forKey: "bundle_id")
        }
        return result
    }

    public static func inputTraits(pid: pid_t) -> [String: Any] {
        let app = NSRunningApplication(processIdentifier: pid)
        let frameworks = app?.bundleURL?.appendingPathComponent("Contents/Frameworks")
        let names = frameworks.flatMap { try? FileManager.default.contentsOfDirectory(atPath: $0.path) } ?? []
        let electron = names.contains("Electron Framework.framework")
        let cef = names.contains { $0.localizedCaseInsensitiveContains("Chromium Embedded Framework") }
        let chromium = names.contains { $0.localizedCaseInsensitiveContains("Chrome Framework") || $0.localizedCaseInsensitiveContains("Chromium Framework") }
        return ["is_electron": electron, "has_cef": cef, "has_chromium_input_surface": electron || cef || chromium,
                "input_traits_evidence": "installed_bundle_framework_names"]
    }

    /// What a drag result says for a Chromium target when the system could not
    /// be made to report the button as pressed (`SystemButtonHold`): the page
    /// then gets mousedown, the moves and mouseup with `buttons` 0 on the moves.
    public static func chromiumDragNote(_ reason: String) -> String {
        "Chromium target: the left button was not reported as pressed for the whole of this drag (\(reason)), so the page received mousedown, the mouse moves and mouseup with event.buttons 0 during some or all of the moves. A page that tracks mousedown and mouseup itself sees the drag; one that tests event.buttons does not. Look again to see which."
    }

    public static func listApps(runningOnly: Bool) -> DesktopResult {
        let running = NSWorkspace.shared.runningApplications.filter { !$0.isTerminated && $0.activationPolicy != .prohibited }
        var apps: [[String: Any]] = running.map { app in
            var data: [String: Any] = ["name": app.localizedName ?? "", "pid": Int(app.processIdentifier), "running": true, "frontmost": app.isActive]
            if let id = app.bundleIdentifier { data["bundle_id"] = id }
            if let url = app.bundleURL { data["path"] = url.path }
            if let date = ProcessIdentity.startDate(pid: app.processIdentifier) { data["launch_date"] = ISO8601DateFormatter().string(from: date) }
            return data
        }
        if !runningOnly {
            var seen = Set(apps.compactMap { $0["path"] as? String })
            let roots = [URL(fileURLWithPath: "/Applications"), URL(fileURLWithPath: "/System/Applications"), FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Applications")]
            for root in roots {
                guard let enumerator = FileManager.default.enumerator(at: root, includingPropertiesForKeys: [.isDirectoryKey], options: [.skipsHiddenFiles]) else { continue }
                for case let url as URL in enumerator {
                    guard url.pathExtension == "app" else { continue }
                    enumerator.skipDescendants()
                    guard seen.insert(url.path).inserted, let bundle = Bundle(url: url), bundle.infoDictionary?["CFBundlePackageType"] as? String == "APPL" else { continue }
                    var data: [String: Any] = ["name": bundle.object(forInfoDictionaryKey: "CFBundleDisplayName") as? String ?? bundle.object(forInfoDictionaryKey: "CFBundleName") as? String ?? url.deletingPathExtension().lastPathComponent, "path": url.path, "running": false]
                    if let id = bundle.bundleIdentifier { data["bundle_id"] = id }
                    apps.append(data)
                }
            }
        }
        return DesktopResult(data: ["apps": apps.sorted { ($0["name"] as? String ?? "").localizedStandardCompare($1["name"] as? String ?? "") == .orderedAscending }, "running_only": runningOnly])
    }

    public static func call(_ name: String, arguments args: [String: Any]) async throws -> DesktopResult {
        switch name {
        case "list_apps": return listApps(runningOnly: args["running_only"] as? Bool ?? false)
        case "list_windows":
            let pid = try resolvePID(args, optional: true)
            return DesktopResult(data: ["windows": windows(pid: pid).map(\.json)])
        case "get_window": return DesktopResult(data: try window(args).json)
        case "launch_app":
            guard let value = args["app"] as? String, !value.isEmpty else { throw LocalCUError.invalidArgument("app must be a bundle identifier or absolute .app path") }
            let url: URL
            if value.hasPrefix("/") {
                url = URL(fileURLWithPath: value)
                guard url.pathExtension == "app", Bundle(url: url) != nil else { throw LocalCUError.invalidArgument("Expected an installed .app bundle") }
            } else {
                guard let found = NSWorkspace.shared.urlForApplication(withBundleIdentifier: value) else { throw LocalCUError.target("Application is not installed") }
                url = found
            }
            let config = NSWorkspace.OpenConfiguration()
            config.activates = false
            let app = try await NSWorkspace.shared.openApplication(at: url, configuration: config)
            return DesktopResult(data: ["pid": Int(app.processIdentifier), "bundle_id": app.bundleIdentifier ?? "", "status": "launched", "path": url.path])
        case "activate_window":
            try DesktopSession.requireUnlockedActivation()
            let selected = try window(args)
            let element = try? axWindow(selected).element
            if let element, try AXReader.writable(element, kAXMinimizedAttribute) {
                let error = AXUIElementSetAttributeValue(element, kAXMinimizedAttribute as CFString, kCFBooleanFalse)
                guard error == .success else { throw LocalCUError.accessibility("Unminimize window", error.rawValue) }
            }
            if element == nil { try await BackgroundInput.prepareWindowFocus(pid: selected.pid, windowID: selected.id) }
            guard let app = NSRunningApplication(processIdentifier: selected.pid), !app.isTerminated else { throw LocalCUError.target("Window owner exited before activation") }
            let activationRequest: String
            if app.isActive { activationRequest = "owner_already_active" }
            else if selected.pid == ProcessInfo.processInfo.processIdentifier {
                NSApplication.shared.activate()
                activationRequest = "self_activation_requested"
            } else {
                activationRequest = "request_accepted=\(app.activate(options: [.activateAllWindows]))"
            }
            // A request's Boolean is not the observed outcome. Even a refused
            // request may race another activation; verify the actual postcondition.
            if let element {
                let error = AXUIElementPerformAction(element, kAXRaiseAction as CFString)
                guard error == .success else { throw LocalCUError.accessibility("Raise window", error.rawValue) }
            }
            for _ in 0..<20 {
                try Task.checkCancellation()
                if NSWorkspace.shared.frontmostApplication?.processIdentifier == selected.pid,
                   windows(pid: selected.pid, onlyOnScreen: true).first(where: { $0.layer == 0 })?.id == selected.id {
                    return DesktopResult(data: ["status": "verified", "window_id": Int(selected.id), "pid": Int(selected.pid),
                        "route": element == nil ? "window_focus_record_and_activation" : "ax_raise_and_activation",
                        "verification": "Target owner is frontmost and the exact CG window is its topmost visible normal window"])
                }
                try await Task.sleep(nanoseconds: 50_000_000)
            }
            throw LocalCUError.operation("Activation did not establish the exact window as topmost (\(activationRequest)); inspect a fresh observation")
        default: throw LocalCUError.invalidArgument("Unknown window tool")
        }
    }
}
