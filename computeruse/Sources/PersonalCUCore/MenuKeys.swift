import AppKit
import ApplicationServices

extension DesktopController {
    /// Return nil when no native menu command matches or the bounded menu search
    /// cannot finish; the chord then goes as a key event. Once an action starts,
    /// never post the key as well: AX acknowledgement is not proof of UI outcome.
    func nativeMenuCommand(_ chord: KeyChord, args: [String: Any], snapshot: Snapshot) async throws -> [String: Any]? {
        guard chord.flags.contains(.maskCommand) else { return nil }
        guard let match = try AXReader.boundedRead({ try findNativeMenuCommand(chord, snapshot: snapshot) }) else { return nil }
        try validateLiveSnapshot(snapshot)
        let focusProof: String
        do { try requireFocusedWindow(snapshot); focusProof = "exact_ax_window" }
        catch is CancellationError { throw CancellationError() }
        catch {
            guard (args["input_mode"] as? String ?? "background") == "background",
                  try AXReader.boundedRead({ try singleDocumentApplicationFocus(snapshot) }) else { return nil }
            try await BackgroundInput.prepareWindowFocus(pid: snapshot.pid, windowID: snapshot.windowID)
            try validateLiveSnapshot(snapshot)
            guard try AXReader.boundedRead({ try singleDocumentApplicationFocus(snapshot) }) else {
                throw LocalCUError.staleSnapshot("Application/window focus evidence changed while preparing the menu action")
            }
            focusProof = "exact_window_focus_record_single_document"
        }
        return try AXReader.boundedRead {
            try validateLiveSnapshot(snapshot)
            guard try menuShortcut(match, matches: chord),
                  try AXReader.value(match, kAXEnabledAttribute) as? Bool != false,
                  try AXReader.actions(match).contains(kAXPressAction) else {
                throw LocalCUError.staleSnapshot("Native menu command changed during lookup")
            }
            let title = try AXReader.string(match, kAXTitleAttribute) ?? ""
            try AXReader.prepare(match)
            let error = AXUIElementPerformAction(match, kAXPressAction as CFString)
            guard error == .success else { throw LocalCUError.accessibility("Press native menu command", error.rawValue) }
            return ["route": "ax_menu", "status": "performed", "title": title, "focus_proof": focusProof,
                    "verification": "Accessibility acknowledged the menu action; inspect the attached observation when available"]
        }
    }

    /// Some providers return their application root for AXFocusedWindow. A
    /// target-only WindowServer focus record plus one unambiguous document is
    /// the bounded alternative; another actual AXWindow is never overridden.
    private func singleDocumentApplicationFocus(_ snapshot: Snapshot) throws -> Bool {
        let windowID = snapshot.windowID
        let application = AXUIElementCreateApplication(snapshot.pid)
        guard let raw = try AXReader.value(application, kAXFocusedWindowAttribute),
              CFGetTypeID(raw) == AXUIElementGetTypeID() else { return false }
        let focused = raw as! AXUIElement
        var owner: pid_t = 0
        guard AXUIElementGetPid(focused, &owner) == .success, owner == snapshot.pid,
              try AXReader.string(focused, kAXRoleAttribute) == kAXApplicationRole else { return false }
        let windows = WindowService.windows(pid: snapshot.pid).filter { $0.layer == 0 }
        let visible = windows.filter { $0.onScreen }
        let titled = windows.filter { !$0.title.isEmpty }
        return visible.count == 1 && visible.first?.id == windowID
            && titled.count == 1 && titled.first?.id == windowID
    }

    private func findNativeMenuCommand(_ chord: KeyChord, snapshot: Snapshot) throws -> AXUIElement? {
        let application = AXUIElementCreateApplication(snapshot.pid)
        let deadline = AXReader.readDeadline ?? ProcessInfo.processInfo.systemUptime + 5
        // A menu bar too large or slow to search is no match, not a failed chord.
        do { return try AXReader.$readDeadline.withValue(deadline) {
            guard let raw = try AXReader.value(application, kAXMenuBarAttribute),
                  CFGetTypeID(raw) == AXUIElementGetTypeID() else { return nil }
            var queue: [(AXUIElement, Int)] = [(raw as! AXUIElement, 0)]
            var cursor = 0
            var visited = Set<CFHashCode>()
            var matches: [AXUIElement] = []
            while cursor < queue.count {
                try Task.checkCancellation()
                guard cursor < 2000, ProcessInfo.processInfo.systemUptime < deadline else { return nil }
                let (element, depth) = queue[cursor]; cursor += 1
                guard visited.insert(CFHash(element)).inserted else { continue }
                if try AXReader.string(element, kAXRoleAttribute) == kAXMenuItemRole,
                   try menuShortcut(element, matches: chord),
                   try AXReader.value(element, kAXEnabledAttribute) as? Bool != false,
                   try AXReader.actions(element).contains(kAXPressAction) { matches.append(element) }
                let children = try AXReader.elements(element, kAXChildrenAttribute)
                guard depth < 32 || children.isEmpty, queue.count + children.count <= 2000 else { return nil }
                queue += children.map { ($0, depth + 1) }
            }
            guard matches.count <= 1 else { throw LocalCUError.target("Native menu shortcut is ambiguous") }
            return matches.first
        } } catch is AXReader.BudgetExceeded { return nil }
    }

    func menuShortcut(_ element: AXUIElement, matches chord: KeyChord) throws -> Bool {
        let mask = CGEventFlags([.maskCommand, .maskShift, .maskAlternate, .maskControl])
        let modifiers = (try AXReader.value(element, kAXMenuItemCmdModifiersAttribute) as? NSNumber)?.uint32Value ?? 0
        guard modifiers & ~UInt32(15) == 0 else { return false }
        var flags: CGEventFlags = modifiers & 8 == 0 ? .maskCommand : []
        if modifiers & 1 != 0 { flags.insert(.maskShift) }
        if modifiers & 2 != 0 { flags.insert(.maskAlternate) }
        if modifiers & 4 != 0 { flags.insert(.maskControl) }
        var key: CGKeyCode?
        if let character = try AXReader.string(element, kAXMenuItemCmdCharAttribute), !character.isEmpty,
           let parsed = try? KeyDSL.parse(character.lowercased()), parsed.count == 1 {
            key = parsed[0].code
            flags.formUnion(parsed[0].flags)
        } else if let number = try AXReader.value(element, kAXMenuItemCmdVirtualKeyAttribute) as? NSNumber,
                  number.doubleValue.isFinite, number.doubleValue.rounded() == number.doubleValue,
                  (0...Double(UInt16.max)).contains(number.doubleValue) { key = CGKeyCode(number.uint16Value) }
        return key == chord.code && flags.intersection(mask) == chord.flags.intersection(mask)
    }

    /// Native Control+A means beginning-of-line in Cocoa; this explicit API uses
    /// the original tool's Select All semantics only for an observable text target.
    func verifiedSelectAll(_ chord: KeyChord, snapshot: Snapshot, target: AXUIElement? = nil) throws -> [String: Any]? {
        guard chord.code == KeyDSL.codes["a"], chord.flags == .maskControl || chord.flags == .maskCommand else { return nil }
        return try AXReader.boundedRead { try boundedSelectAll(snapshot: snapshot, target: target) }
    }

    private func boundedSelectAll(snapshot: Snapshot, target: AXUIElement?) throws -> [String: Any]? {
        let inspected: AXUIElement?
        if let target { inspected = target }
        else {
            do { inspected = try textTarget([:], snapshot) }
            catch is CancellationError { throw CancellationError() }
            catch { return nil }
        }
        guard let element = inspected,
              let role = try AXReader.string(element, kAXRoleAttribute),
              [kAXTextFieldRole, kAXTextAreaRole, kAXComboBoxRole].contains(role),
              try AXReader.string(element, kAXSubroleAttribute) != kAXSecureTextFieldSubrole,
              let value = try AXReader.string(element, kAXValueAttribute) else { return nil }
        guard try AXReader.writable(element, kAXSelectedTextRangeAttribute) else { return nil }
        try validateLiveSnapshot(snapshot)
        var range = CFRange(location: 0, length: (value as NSString).length)
        guard let rangeValue = AXValueCreate(.cfRange, &range) else { throw LocalCUError.operation("Cannot create selection range") }
        try AXReader.prepare(element)
        let error = AXUIElementSetAttributeValue(element, kAXSelectedTextRangeAttribute as CFString, rangeValue)
        guard error == .success else { throw LocalCUError.accessibility("Select all focused text", error.rawValue) }
        guard let actual = try selectedRange(element), actual.location == 0, actual.length == range.length,
              try AXReader.string(element, kAXValueAttribute) == value else {
            throw LocalCUError.operation("Select All readback did not match the full text range")
        }
        return ["route": "ax_select_all", "status": "verified", "selected_utf16_length": range.length]
    }
}
