import AppKit
import ApplicationServices

extension DesktopController {
    func boundedInteger(_ args: [String: Any], _ name: String, default fallback: Int, range: ClosedRange<Int>) throws -> Int {
        let value = args[name] == nil ? fallback : try integer(args, name)
        guard range.contains(value) else { throw LocalCUError.invalidArgument("\(name) must be in \(range)") }
        return value
    }

    func inputMode(_ args: [String: Any], _ snapshot: Snapshot) throws -> String {
        let mode = args["input_mode"] as? String ?? "background"
        guard mode == "foreground" || mode == "background" else { throw LocalCUError.invalidArgument("input_mode must be foreground or background") }
        if mode == "foreground" { try requireSyntheticInput(snapshot) }
        else {
            guard CGPreflightPostEventAccess() else { throw LocalCUError.permission("Post Events permission is required") }
        }
        return mode
    }

    func postMouse(_ events: [CGEvent], delays: [Int]? = nil, args: [String: Any], snapshot: Snapshot) async throws {
        try Task.checkCancellation()
        try validateLiveSnapshot(snapshot)
        if try inputMode(args, snapshot) == "background" {
            try await BackgroundInput.postMouse(events: events, pid: snapshot.pid, windowID: snapshot.windowID, delaysAfterMS: delays)
        } else {
            var down: CGEvent?
            do {
                for (index, event) in events.enumerated() {
                    try Task.checkCancellation()
                    guard NSWorkspace.shared.frontmostApplication?.processIdentifier == snapshot.pid else { throw LocalCUError.target("Target lost foreground focus during input") }
                    event.postToPid(snapshot.pid)
                    if [.leftMouseDown, .rightMouseDown, .otherMouseDown].contains(event.type) { down = event }
                    if [.leftMouseUp, .rightMouseUp, .otherMouseUp].contains(event.type) { down = nil }
                    let delay = delays?[index] ?? 5
                    if delay > 0 { try await Task.sleep(nanoseconds: UInt64(delay) * 1_000_000) }
                }
            } catch {
                if let down, let owner = NSRunningApplication(processIdentifier: snapshot.pid), !owner.isTerminated,
                   ProcessIdentity.startDate(pid: snapshot.pid) == snapshot.launchDate, let up = CGEvent(mouseEventSource: nil,
                                              mouseType: down.type == .rightMouseDown ? .rightMouseUp : down.type == .otherMouseDown ? .otherMouseUp : .leftMouseUp,
                                              mouseCursorPosition: down.location,
                                              mouseButton: down.type == .rightMouseDown ? .right : down.type == .otherMouseDown ? .center : .left) {
                    up.postToPid(snapshot.pid)
                }
                throw error
            }
        }
    }

    func postKey(_ chord: KeyChord, args: [String: Any], snapshot: Snapshot, unicode: [UniChar]? = nil) async throws {
        try await postKeys([(chord, unicode)], args: args, snapshot: snapshot)
    }

    /// Background keys share one exact-window focus preparation; foreground
    /// keys check before each one that the target is still frontmost.
    func postKeys(_ keys: [(chord: KeyChord, unicode: [UniChar]?)], args: [String: Any], snapshot: Snapshot) async throws {
        try Task.checkCancellation()
        try validateLiveSnapshot(snapshot)
        let events = try keys.map { key -> (down: CGEvent, up: CGEvent) in
            guard let source = CGEventSource(stateID: .hidSystemState),
                  let down = CGEvent(keyboardEventSource: source, virtualKey: key.chord.code, keyDown: true),
                  let up = CGEvent(keyboardEventSource: source, virtualKey: key.chord.code, keyDown: false) else {
                throw LocalCUError.operation("Cannot create keyboard events")
            }
            down.flags = key.chord.flags; up.flags = key.chord.flags
            if let unicode = key.unicode {
                unicode.withUnsafeBufferPointer {
                    down.keyboardSetUnicodeString(stringLength: unicode.count, unicodeString: $0.baseAddress!)
                    up.keyboardSetUnicodeString(stringLength: unicode.count, unicodeString: $0.baseAddress!)
                }
            }
            return (down, up)
        }
        if try inputMode(args, snapshot) == "background" {
            try await BackgroundInput.postKeys(events, pid: snapshot.pid, windowID: snapshot.windowID)
            return
        }
        for (index, event) in events.enumerated() {
            if index > 0 {
                try Task.checkCancellation()
                try validateLiveSnapshot(snapshot)
                _ = try inputMode(args, snapshot)
            }
            event.down.postToPid(snapshot.pid); event.up.postToPid(snapshot.pid)
        }
    }

    func targetPoint(_ args: [String: Any], _ snapshot: Snapshot, defaultCenter: Bool = false) throws -> CGPoint {
        if args["index"] != nil {
            guard args["x"] == nil, args["y"] == nil else { throw LocalCUError.invalidArgument("Use index OR x/y") }
            let element = try indexedElement(args, snapshot)
            guard let rect = try AXReader.rectangle(element), !rect.isEmpty,
                  snapshot.windowRect.contains(CGPoint(x: rect.midX, y: rect.midY)) else { throw LocalCUError.target("Element has no center inside the snapshot window") }
            return CGPoint(x: rect.midX, y: rect.midY)
        }
        if args["x"] != nil || args["y"] != nil {
            guard let geometry = snapshot.imageGeometry else { throw LocalCUError.invalidArgument("Coordinates require a screenshot snapshot") }
            return try geometry.globalPoint(x: number(args, "x"), y: number(args, "y"))
        }
        guard defaultCenter else { throw LocalCUError.invalidArgument("Provide index or x/y") }
        return CGPoint(x: snapshot.windowRect.midX, y: snapshot.windowRect.midY)
    }

    func mouseEvent(_ type: CGEventType, at point: CGPoint, button: CGMouseButton = .left, count: Int = 1) throws -> CGEvent {
        guard let event = CGEvent(mouseEventSource: nil, mouseType: type, mouseCursorPosition: point, mouseButton: button) else { throw LocalCUError.operation("Cannot create mouse event") }
        event.flags = []
        event.setIntegerValueField(.mouseEventClickState, value: Int64(count))
        return event
    }

    func advancedClick(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let buttonName = args["button"] as? String ?? "left"
        guard ["left", "right", "middle"].contains(buttonName) else { throw LocalCUError.invalidArgument("Unknown mouse button") }
        let count = try boundedInteger(args, "count", default: 1, range: 1...3)
        let hold = try boundedInteger(args, "hold_ms", default: 40, range: 0...500)
        let hover = try boundedInteger(args, "hover_ms", default: 0, range: 0...1000)
        // AXPress expresses a semantic left click without stealing focus.
        if args["index"] != nil, buttonName == "left", count == 1, args["hold_ms"] == nil, hover == 0 {
            let element = try indexedElement(args, snapshot)
            if try AXReader.actions(element).contains(kAXPressAction) {
                let result = try axPress(args, snapshot)
                return try await observedAfter(result, args: args, snapshot: snapshot)
            }
        }
        let point = try targetPoint(args, snapshot)
        let button: CGMouseButton = buttonName == "right" ? .right : buttonName == "middle" ? .center : .left
        let down: CGEventType = buttonName == "right" ? .rightMouseDown : buttonName == "middle" ? .otherMouseDown : .leftMouseDown
        let up: CGEventType = buttonName == "right" ? .rightMouseUp : buttonName == "middle" ? .otherMouseUp : .leftMouseUp
        var events: [CGEvent] = []; var delays: [Int] = []
        if hover > 0 { events.append(try mouseEvent(.mouseMoved, at: point, button: button)); delays.append(hover) }
        for index in 1...count {
            events += [try mouseEvent(down, at: point, button: button, count: index), try mouseEvent(up, at: point, button: button, count: index)]
            delays += [hold, 30]
        }
        try await postMouse(events, delays: delays, args: args, snapshot: snapshot)
        return try await observedAfter(posted(snapshot, "click"), args: args, snapshot: snapshot)
    }

    func observedAfter(_ original: DesktopResult, args: [String: Any], snapshot: Snapshot) async throws -> DesktopResult {
        guard args["verify_after"] as? Bool == true else { return original }
        // The click was delivered; a failed observation must not read as a
        // failed click, or a caller would repeat it.
        var result = original
        do {
            try await Task.sleep(nanoseconds: 100_000_000)
            let after = try await getState(["pid": Int(snapshot.pid), "window_id": Int(snapshot.windowID), "mode": "full", "disable_diff": true])
            result.data["after"] = after.data
            result.png = after.png
            result.images = after.images
            result.data["verification"] = "A fresh observation is attached; compare it to the intended outcome."
        } catch { result.data["observation_status"] = "unavailable_after_click"; result.data["observation_error"] = error.localizedDescription }
        return result
    }

    func advancedPressKey(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let chords = try KeyDSL.parse(text(args, "keys"))
        _ = try inputMode(args, snapshot)
        var routes: [[String: Any]] = []
        var usedMenu = false
        for chord in chords {
            try Task.checkCancellation()
            try validateLiveSnapshot(snapshot)
            if let selection = try verifiedSelectAll(chord, snapshot: snapshot) { routes.append(selection) }
            else if let menu = try await nativeMenuCommand(chord, args: args, snapshot: snapshot) { routes.append(menu); usedMenu = true }
            else {
                try await postKey(chord, args: args, snapshot: snapshot)
                routes.append(["route": "key_event", "status": "posted_unverified"])
            }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        var result = posted(snapshot, "press_key")
        result.data["chords"] = routes
        if routes.allSatisfy({ $0["status"] as? String == "verified" }) { result.data["status"] = "verified" }
        else if routes.allSatisfy({ $0["status"] as? String != "posted_unverified" }) { result.data["status"] = "performed" }
        if usedMenu {
            do {
                try validateLiveSnapshot(snapshot)
                let after = try await getState(["pid": Int(snapshot.pid), "window_id": Int(snapshot.windowID), "mode": "ax", "disable_diff": true])
                result.data["after"] = after.data
            } catch { result.data["observation_status"] = "unavailable_after_menu_action"; result.data["observation_error"] = error.localizedDescription }
        }
        return result
    }

    func selectedRange(_ element: AXUIElement) throws -> CFRange? {
        guard let value = try AXReader.value(element, kAXSelectedTextRangeAttribute), CFGetTypeID(value) == AXValueGetTypeID() else { return nil }
        var range = CFRange()
        guard AXValueGetValue(value as! AXValue, .cfRange, &range) else { return nil }
        return range
    }

    func selectText(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let element = try indexedElement(args, snapshot)
        guard try AXReader.string(element, kAXSubroleAttribute) != kAXSecureTextFieldSubrole,
              let contents = try AXReader.string(element, kAXValueAttribute) else { throw LocalCUError.target("Element does not expose non-secure text") }
        let value = contents as NSString
        var range: NSRange
        if args["text"] != nil {
            guard args["start"] == nil, args["length"] == nil else { throw LocalCUError.invalidArgument("Use text OR start/length") }
            let needle = try text(args, "text")
            guard !needle.isEmpty else { throw LocalCUError.invalidArgument("Selection text must not be empty") }
            let prefix = args["prefix"] as? String, suffix = args["suffix"] as? String
            let prefixLength = (prefix as NSString?)?.length ?? 0
            let suffixLength = (suffix as NSString?)?.length ?? 0
            let deadline = ProcessInfo.processInfo.systemUptime + 5
            var candidates: [NSRange] = []; var start = 0; var scanned = 0
            while start < value.length {
                try Task.checkCancellation()
                guard scanned < 4096, ProcessInfo.processInfo.systemUptime < deadline else {
                    throw LocalCUError.operation("Selection search exceeded its 4096-match or 5-second budget; use start/length")
                }
                let match = value.range(of: needle, range: NSRange(location: start, length: value.length - start))
                if match.location == NSNotFound { break }
                scanned += 1
                let beforeMatches = prefix == nil || (prefixLength <= match.location &&
                    value.substring(with: NSRange(location: match.location - prefixLength, length: prefixLength)) == prefix!)
                let afterMatches = suffix == nil || (suffixLength <= value.length - NSMaxRange(match) &&
                    value.substring(with: NSRange(location: NSMaxRange(match), length: suffixLength)) == suffix!)
                if beforeMatches && afterMatches { candidates.append(match) }
                start = match.location + max(1, match.length)
                if scanned % 64 == 0 { await Task.yield() }
            }
            guard candidates.count == 1 else { throw LocalCUError.target("Selection text is absent or ambiguous; add prefix/suffix or use start/length") }
            range = candidates[0]
        } else {
            let start = try integer(args, "start"), length = try integer(args, "length")
            guard start >= 0, length >= 0, start <= value.length, length <= value.length - start else { throw LocalCUError.invalidArgument("Selection range is outside the UTF-16 text bounds") }
            range = NSRange(location: start, length: length)
        }
        let selection = args["selection"] as? String ?? "text"
        switch selection {
        case "text": break
        case "cursor_before": range.length = 0
        case "cursor_after": range = NSRange(location: NSMaxRange(range), length: 0)
        default: throw LocalCUError.invalidArgument("selection must be text, cursor_before, or cursor_after")
        }
        guard try AXReader.writable(element, kAXSelectedTextRangeAttribute) else { throw LocalCUError.operation("AXSelectedTextRange is not writable") }
        try validateLiveSnapshot(snapshot)
        _ = try indexedElement(args, snapshot)
        var requested = CFRange(location: range.location, length: range.length)
        guard let encoded = AXValueCreate(.cfRange, &requested) else { throw LocalCUError.operation("Cannot encode selection range") }
        try AXReader.prepare(element)
        let error = AXUIElementSetAttributeValue(element, kAXSelectedTextRangeAttribute as CFString, encoded)
        guard error == .success else { throw LocalCUError.accessibility("Set selection", error.rawValue) }
        guard let actual = try selectedRange(element), actual.location == requested.location, actual.length == requested.length else {
            throw LocalCUError.operation("Selection write was acknowledged but readback did not match")
        }
        return DesktopResult(data: ["status": "verified", "operation": "select_text", "pid": Int(snapshot.pid), "start": range.location, "length": range.length,
                                    "selected_text": value.substring(with: range), "verification": "AXSelectedTextRange readback matched"])
    }

    func secondaryAction(_ args: [String: Any], _ snapshot: Snapshot) throws -> DesktopResult {
        let element = try indexedElement(args, snapshot)
        let action = args["action"] as? String ?? kAXShowMenuAction
        guard action.utf8.count <= 128, try AXReader.actions(element).contains(action) else { throw LocalCUError.operation("Element does not advertise the requested AX action") }
        try AXReader.prepare(element)
        let status = try AXReader.actionStatus(AXUIElementPerformAction(element, action as CFString), "Perform \(action)")
        return DesktopResult(data: ["status": status, "operation": action,
                                    "verification": status == "performed" ? "Accessibility acknowledged the action; re-observe the UI"
                                        : "The app did not answer in time, as while a menu it opened is shown; the action may have happened, so re-observe the UI before any retry."])
    }

    func textTarget(_ args: [String: Any], _ snapshot: Snapshot) throws -> AXUIElement? {
        try AXReader.boundedRead { try inspectedTextTarget(args, snapshot) }
    }

    private func inspectedTextTarget(_ args: [String: Any], _ snapshot: Snapshot) throws -> AXUIElement? {
        if args["index"] != nil { return try indexedElement(args, snapshot) }
        if args["x"] != nil || args["y"] != nil {
            let point = try targetPoint(args, snapshot)
            let candidates = snapshot.descriptors.enumerated().filter {
                guard let rectangle = $0.element.rectangle else { return false }
                return rectangle.contains(point) && [kAXTextFieldRole, kAXTextAreaRole, kAXComboBoxRole].contains($0.element.role)
            }.sorted { ($0.element.rectangle!.width * $0.element.rectangle!.height) < ($1.element.rectangle!.width * $1.element.rectangle!.height) }
            if let target = candidates.first { return try indexedElement(["index": target.offset], snapshot) }
            return nil
        }
        // An application-scope tree cannot prove which AX focus belongs to this
        // CG window. Keep untargeted canvas typing unverified and window-bound.
        guard snapshot.hasAXWindow else { return nil }
        let root = AXUIElementCreateApplication(snapshot.pid)
        guard let focused = try AXReader.value(root, kAXFocusedUIElementAttribute), CFGetTypeID(focused) == AXUIElementGetTypeID() else { return nil }
        let element = focused as! AXUIElement
        var current = element
        var visited = Set<CFHashCode>()
        for _ in 0..<64 {
            try Task.checkCancellation()
            guard visited.insert(CFHash(current)).inserted else {
                throw LocalCUError.target("Focused element parent hierarchy contains a cycle")
            }
            if CFEqual(current, snapshot.window) { return element }
            guard let parent = try AXReader.value(current, kAXParentAttribute), CFGetTypeID(parent) == AXUIElementGetTypeID() else { break }
            current = parent as! AXUIElement
        }
        throw LocalCUError.target("Focused text element belongs to another window; specify index or x/y")
    }

    func advancedTypeText(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let requested = try text(args, "text")
        guard !requested.isEmpty, requested.utf16.count <= 4096 else { throw LocalCUError.invalidArgument("text must contain 1...4096 UTF-16 units; use paste for longer text") }
        try KeyDSL.requirePlainText(requested)
        _ = try inputMode(args, snapshot)
        let candidate = try textTarget(args, snapshot)
        var target: AXUIElement?
        if let candidate, let role = try AXReader.string(candidate, kAXRoleAttribute),
           [kAXTextFieldRole, kAXTextAreaRole, kAXComboBoxRole].contains(role),
           try AXReader.string(candidate, kAXSubroleAttribute) != kAXSecureTextFieldSubrole { target = candidate }
        if args["submit"] as? Bool == true, target == nil {
            throw LocalCUError.operation("submit requires observable text readback; type on the canvas without submit, inspect its screenshot, then press Return explicitly")
        }
        if args["index"] != nil, let candidate, try AXReader.writable(candidate, kAXFocusedAttribute) {
            try AXReader.prepare(candidate)
            let error = AXUIElementSetAttributeValue(candidate, kAXFocusedAttribute as CFString, kCFBooleanTrue)
            guard error == .success else { throw LocalCUError.accessibility("Focus text element", error.rawValue) }
        } else if args["index"] != nil || args["x"] != nil {
            var clickArgs = args; clickArgs["hold_ms"] = 40; clickArgs["verify_after"] = false
            _ = try await advancedClick(clickArgs, snapshot)
        }
        let before = try target.flatMap { try AXReader.string($0, kAXValueAttribute) }
        let range = try target.flatMap { try selectedRange($0) }
        var expected: String?
        var clearRoute: String?
        if args["clear"] as? Bool == true {
            let selectAll = try KeyDSL.parse("command+a")[0]
            if let target, try verifiedSelectAll(selectAll, snapshot: snapshot, target: target) != nil {
                clearRoute = "verified_ax_selection"
            } else if (args["input_mode"] as? String ?? "background") == "background" {
                // Background Cmd+A reaches the DOM with correct flags but does
                // not enter native menu key dispatch. Standard document editing
                // chords work for Chromium inputs/textareas and AppKit text.
                for chord in try KeyDSL.parse("command+up command+shift+down") {
                    try await postKey(chord, args: args, snapshot: snapshot)
                }
                clearRoute = "document_selection_key_events_unverified"
            } else {
                try await postKey(selectAll, args: args, snapshot: snapshot)
                clearRoute = "command_a_key_events_unverified"
            }
            expected = requested
        } else if let before, let range, range.location >= 0, range.length >= 0, range.location + range.length <= (before as NSString).length {
            expected = (before as NSString).replacingCharacters(in: NSRange(location: range.location, length: range.length), with: requested)
        } else if before == "" { expected = requested }
        if args["submit"] as? Bool == true, expected == nil {
            throw LocalCUError.operation("submit requires a known insertion range or clear:true so that text can be verified before Return")
        }
        // Keep each event small, and never split UTF-16 surrogate pairs.
        var chunks: [[UniChar]] = [[]]
        for scalar in requested.unicodeScalars {
            let units = Array(String(scalar).utf16)
            if chunks[chunks.count - 1].count + units.count > 20 { chunks.append([]) }
            chunks[chunks.count - 1].append(contentsOf: units)
        }
        try await postKeys(chunks.map { (KeyChord(code: 0, flags: []), $0) }, args: args, snapshot: snapshot)
        try await Task.sleep(nanoseconds: 100_000_000)
        var result = posted(snapshot, "type_text")
        if let clearRoute {
            result.data["clear_route"] = clearRoute
            result.data["clear_target_chromium"] = WindowService.inputTraits(pid: snapshot.pid)["has_chromium_input_surface"] as? Bool ?? false
        }
        if let target, let after = try AXReader.string(target, kAXValueAttribute) {
            // Cut like a tree value; the comparison below uses the whole text.
            result.data["readback"] = String(after.prefix(4096))
            if let expected { result.data["matches_expected"] = after == expected; result.data["status"] = after == expected ? "verified" : "readback_mismatch" }
            if args["submit"] as? Bool == true {
                if after == expected {
                    try await postKey(KeyDSL.parse("return")[0], args: args, snapshot: snapshot)
                    result.data["submit_status"] = "posted_unverified"
                } else { result.data["submit_status"] = "withheld_readback_mismatch" }
            }
        } else {
            result.data["readback_status"] = "unavailable"
            result.data["verification"] = "Unicode events posted to the bound window; inspect a fresh screenshot to verify canvas text."
        }
        return result
    }

    func advancedScroll(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let point = try targetPoint(args, snapshot, defaultCenter: true)
        let dy: Int, dx: Int
        if args["page"] != nil {
            guard args["delta_x"] == nil, args["delta_y"] == nil else { throw LocalCUError.invalidArgument("Use page OR pixel deltas") }
            let pages = try boundedInteger(args, "page", default: 0, range: -20...20)
            guard pages != 0 else { throw LocalCUError.invalidArgument("page must be nonzero") }
            let delta = Double(pages) * snapshot.windowRect.height * 0.8
            guard delta.isFinite, delta >= Double(Int32.min), delta <= Double(Int32.max) else { throw LocalCUError.invalidArgument("Page scroll exceeds the supported pixel range") }
            dy = Int(delta); dx = 0
        } else {
            dy = try boundedInteger(args, "delta_y", default: 0, range: -1000...1000)
            dx = try boundedInteger(args, "delta_x", default: 0, range: -1000...1000)
            guard dy != 0 || dx != 0 else { throw LocalCUError.invalidArgument("At least one scroll delta must be nonzero") }
        }
        guard let event = CGEvent(scrollWheelEvent2Source: nil, units: .pixel, wheelCount: 2, wheel1: Int32(clamping: dy), wheel2: Int32(clamping: dx), wheel3: 0) else { throw LocalCUError.operation("Cannot create scroll event") }
        event.location = point; event.flags = []
        try await postMouse([event], args: args, snapshot: snapshot)
        return posted(snapshot, "scroll")
    }

    func drag(_ args: [String: Any], _ snapshot: Snapshot, multiple: Bool) async throws -> DesktopResult {
        guard let geometry = snapshot.imageGeometry else { throw LocalCUError.invalidArgument("Drag requires a screenshot snapshot") }
        let rawPaths: [[String: Any]]
        if multiple {
            guard let provided = args["paths"] as? [[String: Any]], !provided.isEmpty, provided.count <= 500 else { throw LocalCUError.invalidArgument("paths must contain 1...500 path objects") }
            rawPaths = provided
        } else {
            rawPaths = [["points": [[try number(args, "from_x"), try number(args, "from_y")], [try number(args, "to_x"), try number(args, "to_y")]],
                         "steps": args["steps"] ?? 12, "hold_ms": args["hold_ms"] ?? 0, "step_ms": args["step_ms"] ?? 12]]
        }
        var plans: [(events: [CGEvent], delays: [Int])] = []; var totalTime = 0; var totalPoints = 0
        for path in rawPaths {
            guard let values = path["points"] as? [[Any]], values.count >= 2 else { throw LocalCUError.invalidArgument("Each path needs at least two [x,y] points") }
            totalPoints += values.count
            guard totalPoints <= 1024 else { throw LocalCUError.invalidArgument("paths exceed 1024 total points") }
            let points = try values.map { value -> CGPoint in
                guard value.count == 2 else { throw LocalCUError.invalidArgument("Each point must be [x,y]") }
                return try geometry.globalPoint(x: number(["x": value[0]], "x"), y: number(["y": value[1]], "y"))
            }
            let steps = try boundedInteger(path, "steps", default: 12, range: 1...500)
            let hold = try boundedInteger(path, "hold_ms", default: 0, range: 0...1000)
            let interval = try boundedInteger(path, "step_ms", default: 12, range: 0...100)
            let eventCount = (points.count - 1) * steps + 2
            guard eventCount <= 4096 else { throw LocalCUError.invalidArgument("One drag path exceeds 4096 interpolated events") }
            totalTime += 200 + hold + (eventCount - 2) * interval
            guard totalTime <= 20_000 else { throw LocalCUError.invalidArgument("Drag paths exceed the 20-second total duration limit") }
            var events = [try mouseEvent(.leftMouseDown, at: points[0])]; var delays = [hold]
            for segment in 1..<points.count {
                for step in 1...steps {
                    let fraction = Double(step) / Double(steps)
                    let point = CGPoint(x: points[segment - 1].x + (points[segment].x - points[segment - 1].x) * fraction,
                                        y: points[segment - 1].y + (points[segment].y - points[segment - 1].y) * fraction)
                    events.append(try mouseEvent(.leftMouseDragged, at: point)); delays.append(interval)
                }
            }
            events.append(try mouseEvent(.leftMouseUp, at: points.last!)); delays.append(0)
            plans.append((events, delays))
        }
        let initialCursor = CGEvent(source: nil)?.location
        let chromium = WindowService.inputTraits(pid: snapshot.pid)["has_chromium_input_surface"] as? Bool == true
        var notHeld: String?
        for plan in plans {
            try Task.checkCancellation()
            if multiple, args["abort_if_cursor_in_window"] as? Bool != false,
               let cursor = CGEvent(source: nil)?.location, snapshot.windowRect.contains(cursor) {
                throw LocalCUError.operation("Drag aborted because the user's cursor is inside the target window")
            }
            if multiple, let initialCursor, let cursor = CGEvent(source: nil)?.location,
               hypot(cursor.x - initialCursor.x, cursor.y - initialCursor.y) > 4 { throw LocalCUError.operation("Drag aborted because the user's cursor moved") }
            // What postMouse refuses is refused before any press reaches the system.
            try validateLiveSnapshot(snapshot)
            _ = try inputMode(args, snapshot)
            let refused = chromium ? await SystemButtonHold.hold() : "not a Chromium target"
            defer { if refused == nil { SystemButtonHold.release() } }
            notHeld = notHeld ?? refused
            try await postMouse(plan.events, delays: plan.delays, args: args, snapshot: snapshot)
            if refused == nil, NSEvent.pressedMouseButtons & 1 == 0 {
                notHeld = notHeld ?? "the pressed state ended during the drag, as it may when the user clicks"
            }
        }
        var result = posted(snapshot, multiple ? "drag_paths" : "drag")
        result.data["paths_posted"] = plans.count
        if chromium {
            if let notHeld { result.data["button_state_note"] = WindowService.chromiumDragNote(notHeld) }
            else { result.data["button_state"] = "left_button_reported_pressed" }
        }
        return result
    }

    func paste(_ args: [String: Any], _ snapshot: Snapshot) async throws -> DesktopResult {
        let contents = try text(args, "text")
        guard contents.utf8.count <= 2 * 1024 * 1024 else { throw LocalCUError.invalidArgument("Paste exceeds 2 MiB") }
        let format = args["format"] as? String ?? "text"
        guard ["text", "md", "html"].contains(format) else { throw LocalCUError.invalidArgument("format must be text, md, or html") }
        _ = try inputMode(args, snapshot)
        let payload: [(NSPasteboard.PasteboardType, Data)]
        if format == "md" { payload = try MarkdownPaste.payload(contents) }
        else if format == "html" { payload = [(.string, Data(contents.utf8)), (.html, Data(contents.utf8))] }
        else { payload = [(.string, Data(contents.utf8))] }
        guard payload.reduce(0, { $0 + $1.1.count }) <= 16 * 1024 * 1024 else { throw LocalCUError.operation("Rendered paste content exceeds the 16-MiB clipboard budget") }
        let item = NSPasteboardItem()
        for (type, data) in payload {
            guard item.setData(data, forType: type) else { throw LocalCUError.operation("Cannot prepare temporary pasteboard content") }
        }
        let board = NSPasteboard.general
        let initialChange = board.changeCount
        var totalBytes = 0
        let saved = try (board.pasteboardItems ?? []).map { item in try item.types.map { type -> (NSPasteboard.PasteboardType, Data) in
            guard let data = item.data(forType: type) else { throw LocalCUError.operation("Clipboard contains a flavor that cannot be preserved") }
            totalBytes += data.count
            guard totalBytes <= 16 * 1024 * 1024 else { throw LocalCUError.operation("Existing clipboard exceeds the 16-MiB preservation budget") }
            return (type, data)
        } }
        guard board.changeCount == initialChange else { throw LocalCUError.operation("Clipboard changed while being preserved; paste was not attempted") }
        board.clearContents()
        var ownedChange = board.changeCount
        var restoreStatus: String?
        // Puts the saved clipboard back unless someone else wrote to it since.
        func restore() -> String {
            guard board.changeCount == ownedChange else { return "preserved_external_change" }
            var complete = true
            let items = saved.map { types -> NSPasteboardItem in
                let item = NSPasteboardItem()
                for (type, data) in types where !item.setData(data, forType: type) { complete = false }
                return item
            }
            board.clearContents()
            guard items.isEmpty || board.writeObjects(items), complete else { return "restore_failed" }
            return "restored"
        }
        defer { if restoreStatus == nil { _ = restore() } }
        let wrote = board.writeObjects([item])
        ownedChange = board.changeCount
        guard wrote else { throw LocalCUError.operation("Cannot write temporary pasteboard content") }
        let chord = try KeyDSL.parse("command+v")[0]
        let menu = try await nativeMenuCommand(chord, args: args, snapshot: snapshot)
        if menu == nil { try await postKey(chord, args: args, snapshot: snapshot) }
        try await Task.sleep(nanoseconds: 250_000_000)
        let status = restore()
        restoreStatus = status
        var result = posted(snapshot, "paste")
        if let menu { result.data["command"] = menu; result.data["status"] = "performed" }
        result.data["clipboard_restore"] = status
        return result
    }
}
