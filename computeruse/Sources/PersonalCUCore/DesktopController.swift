import AppKit
import ApplicationServices
import ScreenCaptureKit
import ImageIO
import UniformTypeIdentifiers

@MainActor
public final class DesktopController {
    struct Snapshot {
        let id: String
        let pid: pid_t
        let launchDate: Date
        let created: TimeInterval
        let window: AXUIElement
        let hasAXWindow: Bool
        let axScope: String
        let axError: String?
        let windowRect: CGRect
        let elements: [AXUIElement]
        let descriptors: [AXReader.ElementDescriptor]
        let imageGeometry: ScreenshotGeometry?
        let screenshotID: String?
        let windowID: UInt32
        let windowIndex: Int
        let title: String
        let treeData: [[String: Any]]
        let treeTruncated: Bool
        let previousTree: [[String: Any]]?
        let diffEnabled: Bool
    }

    var snapshots: [String: Snapshot] = [:]
    var previousTrees: [String: [[String: Any]]] = [:]
    var previousTreeOrder: [String] = []
    let lifetime: TimeInterval = 30

    public init() {}

    public func call(_ name: String, arguments args: [String: Any]) async throws -> DesktopResult {
        switch name {
        case "doctor":
            return DesktopResult(data: [
                "accessibility": AXIsProcessTrusted(),
                "screen_recording": CGPreflightScreenCaptureAccess(),
                "post_events": CGPreflightPostEventAccess(),
                "permission_prompts_requested": false,
                "platform": "macOS",
                "snapshot_ttl_seconds": Int(lifetime),
                "default_input_mode": "background",
                "background_input": BackgroundInput.available(),
                "supported_keys": KeyDSL.codes.keys.sorted()
            ])
        case "activate_app":
            try DesktopSession.requireUnlockedActivation()
            let app = try resolveApp(args)
            guard app.activate(options: [.activateAllWindows]) else { throw LocalCUError.operation("The target application refused activation") }
            for _ in 0..<20 {
                if NSWorkspace.shared.frontmostApplication?.processIdentifier == app.processIdentifier {
                    return DesktopResult(data: ["status": "verified", "verification": "frontmost_application", "app": appData(app)])
                }
                try await Task.sleep(nanoseconds: 50_000_000)
            }
            throw LocalCUError.operation("Activation was requested, but the target did not become frontmost")
        case "get_app_state": return try await getState(args)
        case "click", "set_value", "type_text", "press_key", "scroll", "select_text", "perform_secondary_action", "drag", "drag_paths", "paste":
            try AXReader.checkPermission()
            let snapshot = try validatedSnapshot(args)
            // Consume authority before the first await. Concurrent reads may create a fresh snapshot.
            snapshots.removeValue(forKey: snapshot.id)
            switch name {
            case "click": return try await advancedClick(args, snapshot)
            case "set_value": return try await setValue(args, snapshot)
            case "type_text": return try await advancedTypeText(args, snapshot)
            case "press_key": return try await advancedPressKey(args, snapshot)
            case "scroll": return try await advancedScroll(args, snapshot)
            case "select_text": return try await selectText(args, snapshot)
            case "perform_secondary_action": return try secondaryAction(args, snapshot)
            case "drag", "drag_paths": return try await drag(args, snapshot, multiple: name == "drag_paths")
            default: return try await paste(args, snapshot)
            }
        default: throw LocalCUError.invalidArgument("Unknown tool: \(name)")
        }
    }

    func appData(_ app: NSRunningApplication) -> [String: Any] {
        var data: [String: Any] = ["pid": Int(app.processIdentifier), "name": app.localizedName ?? "", "frontmost": app.isActive]
        if let bundle = app.bundleIdentifier { data["bundle_id"] = bundle }
        if let date = ProcessIdentity.startDate(pid: app.processIdentifier) { data["launch_date"] = ISO8601DateFormatter().string(from: date) }
        return data
    }

    func integer(_ args: [String: Any], _ name: String) throws -> Int {
        guard let value = args[name] as? NSNumber, CFGetTypeID(value) != CFBooleanGetTypeID(),
              value.doubleValue.isFinite, value.doubleValue.rounded() == value.doubleValue,
              value.doubleValue >= Double(Int32.min), value.doubleValue <= Double(Int32.max) else {
            throw LocalCUError.invalidArgument("\(name) must be an integer")
        }
        return value.intValue
    }

    func number(_ args: [String: Any], _ name: String) throws -> Double {
        guard let value = args[name] as? NSNumber, CFGetTypeID(value) != CFBooleanGetTypeID(), value.doubleValue.isFinite else {
            throw LocalCUError.invalidArgument("\(name) must be a finite number")
        }
        return value.doubleValue
    }

    func text(_ args: [String: Any], _ name: String) throws -> String {
        guard let value = args[name] as? String else { throw LocalCUError.invalidArgument("\(name) must be a string") }
        return value
    }

    func resolveApp(_ args: [String: Any]) throws -> NSRunningApplication {
        guard (args["pid"] == nil) != (args["bundle_id"] == nil) else {
            throw LocalCUError.invalidArgument("Provide exactly one of pid or bundle_id")
        }
        let app: NSRunningApplication
        if args["pid"] != nil {
            let pid = try integer(args, "pid")
            guard pid > 0, let candidate = NSRunningApplication(processIdentifier: pid_t(pid)), !candidate.isTerminated else {
                throw LocalCUError.target("No running application has that pid")
            }
            app = candidate
        } else {
            let identifier = try text(args, "bundle_id")
            let matches = NSRunningApplication.runningApplications(withBundleIdentifier: identifier).filter { !$0.isTerminated }
            guard matches.count == 1, let candidate = matches.first else {
                throw LocalCUError.target("bundle_id must identify exactly one running application; use list_apps and pid")
            }
            app = candidate
        }
        guard ProcessIdentity.startDate(pid: app.processIdentifier) != nil else { throw LocalCUError.target("Cannot establish target process launch identity") }
        return app
    }

    func getState(_ args: [String: Any]) async throws -> DesktopResult {
        if let id = args["snapshot_id"] as? String {
            guard let snapshot = snapshots[id] else { throw LocalCUError.staleSnapshot("Unknown snapshot for pagination") }
            if args["window_id"] != nil {
                let requestedWindow = try number(args, "window_id")
                guard requestedWindow.rounded() == requestedWindow, requestedWindow > 0,
                      requestedWindow <= Double(UInt32.max), UInt32(requestedWindow) == snapshot.windowID else {
                    throw LocalCUError.staleSnapshot("window_id does not match pagination snapshot")
                }
            }
            var validation = args
            if args["bundle_id"] != nil { validation["pid"] = Int(try resolveApp(args).processIdentifier) }
            else { validation["pid"] = args["pid"] ?? Int(snapshot.pid) }
            _ = try validatedSnapshot(validation)
            return try statePage(snapshot, args: args, includeImage: false)
        }
        let app = try resolveApp(args)
        guard let launchDate = ProcessIdentity.startDate(pid: app.processIdentifier) else { throw LocalCUError.target("Cannot establish target process launch identity") }
        let mode = (args["mode"] as? String) ?? "ax"
        guard ["ax", "full", "image"].contains(mode) else { throw LocalCUError.invalidArgument("mode must be ax, full, or image") }
        let cgWindows = WindowService.windows(pid: app.processIdentifier)
        let selected: WindowService.Window
        if args["window_id"] != nil { selected = try WindowService.window(args) }
        else {
            let normal = cgWindows.filter { $0.layer == 0 }
            let candidates = normal.filter { $0.onScreen } + normal.filter { !$0.onScreen }
            guard let first = candidates.first else { throw LocalCUError.target("The target has no normal CG window") }
            selected = first
        }
        let rect = selected.rectangle
        guard rect.minX.isFinite, rect.minY.isFinite, rect.width.isFinite, rect.height.isFinite,
              rect.width > 0, rect.height > 0 else { throw LocalCUError.target("Target CG window has no usable geometry") }
        let root = AXUIElementCreateApplication(app.processIdentifier)
        var window = root
        var hasAXWindow = false
        var index = -1
        var axError: String?
        var tree = AXReader.Tree()
        // Image capture is independent of accessibility window discovery.
        if mode != "image" {
            try AXReader.checkPermission()
            do {
                try AXReader.boundedRead {
                    let windows = try AXReader.elements(root, kAXWindowsAttribute)
                    let matches = try windows.enumerated().filter { _, candidate in
                        guard try AXReader.string(candidate, kAXRoleAttribute) == kAXWindowRole,
                              let frame = try AXReader.rectangle(candidate) else { return false }
                        return sameRectangle(frame, rect, tolerance: 2)
                    }
                    let titled = try matches.filter { try AXReader.string($0.element, kAXTitleAttribute) == selected.title }
                    let candidates = titled.isEmpty ? matches : titled
                    if candidates.count == 1, let match = candidates.first {
                        window = match.element; index = match.offset; hasAXWindow = true
                    }
                }
            } catch is CancellationError { throw CancellationError() }
            catch { axError = error.localizedDescription }
            // A failed read is an error: an empty or partial tree saved as a
            // snapshot would let a caller act on controls it never saw.
            tree = try AXReader.tree(window)
        }
        var png: Data?
        var geometry: ScreenshotGeometry?
        if mode != "ax" {
            let capture = try await captureWindow(pid: app.processIdentifier, title: selected.title, rect: rect, windowID: selected.id, args: args)
            png = capture.0; geometry = capture.1
        }
        // Each cause says itself: "changed" alone left a refusal unexplained.
        guard !app.isTerminated, ProcessIdentity.startDate(pid: app.processIdentifier) == launchDate else {
            throw LocalCUError.staleSnapshot("Target process exited or restarted while reading state; request another snapshot")
        }
        guard let current = WindowService.windows(pid: app.processIdentifier).first(where: { $0.id == selected.id }) else {
            throw LocalCUError.staleSnapshot("Target CG window closed while reading state; request another snapshot")
        }
        guard sameWindowFrame(rect, current.rectangle) else {
            throw LocalCUError.staleSnapshot("Target CG window changed while reading state (it moved or resized from \(rectangleData(rect)) to \(rectangleData(current.rectangle))); request another snapshot")
        }
        let now = ProcessInfo.processInfo.systemUptime
        snapshots = snapshots.filter {
            now - $0.value.created < lifetime && !($0.value.pid == app.processIdentifier && $0.value.windowID == selected.id)
        }
        if snapshots.count >= 8, let oldest = snapshots.min(by: { $0.value.created < $1.value.created }) { snapshots.removeValue(forKey: oldest.key) }
        let id = UUID().uuidString
        let axScope = mode == "image" ? "none" : hasAXWindow ? "window" : "application"
        let treeKey = "\(app.processIdentifier):\(selected.id):\(axScope)"
        let snapshot = Snapshot(id: id, pid: app.processIdentifier, launchDate: launchDate, created: now,
                                window: window, hasAXWindow: hasAXWindow, axScope: axScope, axError: axError,
                                windowRect: rect, elements: tree.elements, descriptors: tree.descriptors,
                                imageGeometry: geometry, screenshotID: png == nil ? nil : UUID().uuidString,
                                windowID: selected.id, windowIndex: index, title: selected.title,
                                treeData: tree.data, treeTruncated: tree.truncated,
                                previousTree: previousTrees[treeKey], diffEnabled: args["disable_diff"] as? Bool != true)
        snapshots[id] = snapshot
        // The screenshot goes out with this first page only, so no snapshot keeps it.
        var result = try statePage(snapshot, args: args, includeImage: true, png: png)
        result.data["mode"] = mode
        result.data["window_count"] = cgWindows.filter { $0.layer == 0 }.count
        if let identifiers = args["additional_window_ids"] as? [NSNumber] {
            guard identifiers.count <= 3 else { throw LocalCUError.invalidArgument("At most 3 additional windows") }
            var additional: [[String: Any]] = []
            for identifier in identifiers {
                guard CFGetTypeID(identifier) != CFBooleanGetTypeID(), identifier.doubleValue == Double(identifier.uint32Value),
                      let extra = WindowService.windows(pid: app.processIdentifier).first(where: { $0.id == identifier.uint32Value }) else {
                    throw LocalCUError.target("Additional window must belong to the same application")
                }
                let captured = try await captureWindow(pid: app.processIdentifier, title: extra.title, rect: extra.rectangle, windowID: extra.id, args: [:])
                result.images.append(captured.0)
                additional.append(["window_id": Int(extra.id), "screenshot_id": UUID().uuidString,
                                   "pixel_width": captured.1.pixelWidth, "pixel_height": captured.1.pixelHeight,
                                   "window_rect_points": rectangleData(captured.1.windowRect)])
            }
            result.data["additional_screenshots"] = additional
        }
        guard snapshots[id] != nil else { throw LocalCUError.staleSnapshot("Observation was superseded while capturing additional windows") }
        try validateLiveSnapshot(snapshot)
        return result
    }

    func captureWindow(pid: pid_t, title: String, rect: CGRect, windowID: UInt32? = nil, args: [String: Any] = [:]) async throws -> (Data, ScreenshotGeometry) {
        guard CGPreflightScreenCaptureAccess() else { throw LocalCUError.permission("Enable Screen Recording for the host application or executable. No permission prompt was requested.") }
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: true)
        let matches = content.windows.filter { $0.owningApplication?.processID == pid && (windowID == nil || $0.windowID == windowID) && sameRectangle($0.frame, rect, tolerance: 2) }
        let titleMatches = matches.filter { ($0.title ?? "") == title }
        let candidates = titleMatches.isEmpty ? matches : titleMatches
        guard candidates.count == 1, let window = candidates.first else {
            throw LocalCUError.target("Cannot match the target CG window to one on-screen capture window unambiguously")
        }
        let filter = SCContentFilter(desktopIndependentWindow: window)
        let scale = Double(filter.pointPixelScale)
        guard scale > 0, scale.isFinite else { throw LocalCUError.operation("Invalid capture pixel scale") }
        let configuration = SCStreamConfiguration()
        guard rect.width * scale <= 32768, rect.height * scale <= 32768,
              rect.width * rect.height * scale * scale <= 100_000_000 else { throw LocalCUError.operation("Window exceeds the 100-megapixel capture budget") }
        configuration.width = max(1, Int((rect.width * scale).rounded()))
        configuration.height = max(1, Int((rect.height * scale).rounded()))
        configuration.showsCursor = false
        configuration.ignoreShadowsSingleWindow = true
        configuration.ignoreGlobalClipSingleWindow = true
        configuration.scalesToFit = true
        if #available(macOS 14.2, *) { configuration.includeChildWindows = false }
        var image = try await SCScreenshotManager.captureImage(contentFilter: filter, configuration: configuration)
        var capturedRect = window.frame
        if let crop = args["rect"] as? [String: Any] {
            let x = try number(crop, "x"), y = try number(crop, "y")
            let width = try number(crop, "width"), height = try number(crop, "height")
            guard x >= 0, y >= 0, width > 0, height > 0, x + width <= rect.width, y + height <= rect.height else {
                throw LocalCUError.invalidArgument("rect must fit within the target window in local points")
            }
            let sx = Double(image.width) / rect.width, sy = Double(image.height) / rect.height
            let pixels = CGRect(x: x * sx, y: y * sy, width: width * sx, height: height * sy).integral
                .intersection(CGRect(x: 0, y: 0, width: image.width, height: image.height))
            guard let cropped = image.cropping(to: pixels) else { throw LocalCUError.operation("Image crop failed") }
            image = cropped
            capturedRect = CGRect(x: window.frame.minX + pixels.minX / sx, y: window.frame.minY + pixels.minY / sy,
                                  width: pixels.width / sx, height: pixels.height / sy)
        }
        let factor = min(1, 2048 / Double(max(image.width, image.height)), 1536 / Double(min(image.width, image.height)))
        if factor < 1 {
            let width = max(1, Int((Double(image.width) * factor).rounded()))
            let height = max(1, Int((Double(image.height) * factor).rounded()))
            guard let context = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                                          space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue) else {
                throw LocalCUError.operation("Cannot resize captured image")
            }
            context.interpolationQuality = .high
            context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
            guard let resized = context.makeImage() else { throw LocalCUError.operation("Image resizing failed") }
            image = resized
        }
        let output = NSMutableData()
        guard let destination = CGImageDestinationCreateWithData(output, UTType.png.identifier as CFString, 1, nil) else {
            throw LocalCUError.operation("Cannot create PNG encoder")
        }
        CGImageDestinationAddImage(destination, image, nil)
        guard CGImageDestinationFinalize(destination) else { throw LocalCUError.operation("PNG encoding failed") }
        return (output as Data, ScreenshotGeometry(windowRect: capturedRect, pixelWidth: image.width, pixelHeight: image.height))
    }

    func validatedSnapshot(_ args: [String: Any]) throws -> Snapshot {
        let id = try text(args, "snapshot_id")
        let pid = try integer(args, "pid")
        guard let snapshot = snapshots[id] else { throw LocalCUError.staleSnapshot("Unknown or expired snapshot_id; request get_app_state") }
        if let screenshotID = args["screenshot_id"] as? String, screenshotID != snapshot.screenshotID {
            throw LocalCUError.staleSnapshot("screenshot_id does not match this snapshot")
        }
        try validateLiveSnapshot(snapshot, requestedPID: pid)
        return snapshot
    }

    func validateLiveSnapshot(_ snapshot: Snapshot, requestedPID: Int? = nil) throws {
        let app = NSRunningApplication(processIdentifier: snapshot.pid)
        do {
            try SnapshotIdentity(pid: snapshot.pid, launchDate: snapshot.launchDate, created: snapshot.created)
                .validate(requestedPID: requestedPID ?? Int(snapshot.pid), currentLaunchDate: ProcessIdentity.startDate(pid: snapshot.pid), terminated: app?.isTerminated ?? true,
                          now: ProcessInfo.processInfo.systemUptime, lifetime: lifetime)
        } catch {
            snapshots.removeValue(forKey: snapshot.id)
            throw error
        }
        guard let current = WindowService.window(id: snapshot.windowID, pid: snapshot.pid),
              sameWindowFrame(current.rectangle, snapshot.windowRect) else {
            snapshots.removeValue(forKey: snapshot.id)
            throw LocalCUError.staleSnapshot("CG window ownership or geometry changed; request get_app_state")
        }
    }

    func indexedElement(_ args: [String: Any], _ snapshot: Snapshot) throws -> AXUIElement {
        let index = try integer(args, "index")
        guard index >= 0, index < snapshot.elements.count else { throw LocalCUError.invalidArgument("index is outside snapshot elements") }
        let element = snapshot.elements[index]
        var pid: pid_t = 0
        let result = AXUIElementGetPid(element, &pid)
        guard result == .success, pid == snapshot.pid else { throw LocalCUError.staleSnapshot("Element no longer belongs to target") }
        guard try AXReader.descriptor(element) == snapshot.descriptors[index] else {
            throw LocalCUError.staleSnapshot("Element content, identity, or geometry changed; request get_app_state")
        }
        if !snapshot.hasAXWindow {
            guard let rect = snapshot.descriptors[index].rectangle, !rect.isEmpty,
                  snapshot.windowRect.contains(CGPoint(x: rect.midX, y: rect.midY)) else {
                throw LocalCUError.target("Application-scope AX element is not geometrically bound to this CG window")
            }
        }
        return element
    }

    func requireSyntheticInput(_ snapshot: Snapshot) throws {
        guard CGPreflightPostEventAccess() else { throw LocalCUError.permission("Posting input events is unavailable; enable Accessibility for the hosting application") }
        guard NSWorkspace.shared.frontmostApplication?.processIdentifier == snapshot.pid else {
            throw LocalCUError.target("needs_activation: synthetic input requires the target to be frontmost; call activate_app, then get_app_state")
        }
        try requireFocusedWindow(snapshot)
    }

    func requireFocusedWindow(_ snapshot: Snapshot) throws {
        try AXReader.boundedRead {
            let root = AXUIElementCreateApplication(snapshot.pid)
            guard let value = try AXReader.value(root, kAXFocusedWindowAttribute), CFGetTypeID(value) == AXUIElementGetTypeID() else {
                throw LocalCUError.target("Cannot prove the target's exact focused window; use background input with window_id")
            }
            let focused = value as! AXUIElement
            if snapshot.hasAXWindow, CFEqual(focused, snapshot.window) { return }
            if let number = try AXReader.value(focused, "AXWindowNumber") as? NSNumber,
               CFGetTypeID(number) != CFBooleanGetTypeID(), number.doubleValue == Double(number.uint32Value),
               number.uint32Value == snapshot.windowID { return }
            throw LocalCUError.target("Cannot prove the focused AX window matches this CG window; request a matching observation or use background input")
        }
    }

    func posted(_ snapshot: Snapshot, _ operation: String) -> DesktopResult {
        DesktopResult(data: ["status": "posted_unverified", "operation": operation, "pid": Int(snapshot.pid),
                             "verification": "Events were posted to the target process; request get_app_state to inspect the result."])
    }

    func axPress(_ args: [String: Any], _ snapshot: Snapshot) throws -> DesktopResult {
        let element = try indexedElement(args, snapshot)
        guard try AXReader.actions(element).contains(kAXPressAction) else { throw LocalCUError.operation("Element does not support AXPress") }
        let status = try AXReader.actionStatus(AXUIElementPerformAction(element, kAXPressAction as CFString), "AXPress")
        return DesktopResult(data: ["status": status, "operation": "AXPress", "pid": Int(snapshot.pid),
                                   "verification": status == "performed" ? "Accessibility acknowledged the action; inspect a new snapshot for UI outcome."
                                       : "The app did not answer in time, as while a menu it opened is shown; the press may have happened, so inspect a new snapshot before any retry."])
    }

    func setValue(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let element = try indexedElement(args, snapshot)
        let requested = try text(args, "value")
        guard requested.utf8.count <= 65_536 else { throw LocalCUError.invalidArgument("value exceeds 65536 UTF-8 bytes") }
        guard try AXReader.string(element, kAXSubroleAttribute) != kAXSecureTextFieldSubrole else {
            throw LocalCUError.operation("Secure text fields are not exposed for set_value")
        }
        guard try AXReader.writable(element, kAXValueAttribute) else { throw LocalCUError.operation("AXValue is not writable on this element") }
        let error = AXUIElementSetAttributeValue(element, kAXValueAttribute as CFString, requested as CFString)
        guard error == .success else { throw LocalCUError.accessibility("Write AXValue", error.rawValue) }
        let readback = try AXReader.string(element, kAXValueAttribute)
        guard readback == requested else { throw LocalCUError.operation("AXValue write was acknowledged, but immediate readback did not match") }
        var data: [String: Any] = ["status": "verified", "operation": "set_value", "pid": Int(snapshot.pid), "verification": "AXValue readback matched", "value": requested]
        if args["autosubmit"] as? Bool == true {
            if try AXReader.actions(element).contains(kAXConfirmAction) {
                try AXReader.prepare(element)
                let status = AXUIElementPerformAction(element, kAXConfirmAction as CFString)
                guard status == .success else { throw LocalCUError.accessibility("Value verified, but AXConfirm failed", status.rawValue) }
                data["submit_status"] = "performed"
                data["submit_method"] = "AXConfirm"
            } else {
                guard try AXReader.writable(element, kAXFocusedAttribute) else { throw LocalCUError.operation("Value verified, but autosubmit cannot focus this control") }
                let status = AXUIElementSetAttributeValue(element, kAXFocusedAttribute as CFString, kCFBooleanTrue)
                guard status == .success else { throw LocalCUError.accessibility("Value verified, but autosubmit focus failed", status.rawValue) }
                try await postKey(KeyDSL.parse("return")[0], args: args, snapshot: snapshot)
                data["submit_status"] = "posted_unverified"
                data["submit_method"] = "return"
            }
        }
        return DesktopResult(data: data)
    }

}
