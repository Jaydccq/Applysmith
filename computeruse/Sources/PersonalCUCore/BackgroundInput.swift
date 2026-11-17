// Background event recipes adapted from MIT Cua Driver and its MIT yabai
// focus-record mechanism. See NOTICE and docs/architecture/computer-use.md at
// the repository root.
import AppKit
import ApplicationServices
import CoreGraphics
import Darwin
import ObjectiveC

/// A PID/window-addressed route. A successful post means attempted delivery;
/// callers must observe the application to establish that the action took effect.
@MainActor
public enum BackgroundInput {
    private typealias Post = @convention(c) (Int32, UnsafeMutableRawPointer) -> Void
    private typealias SetInteger = @convention(c) (UnsafeMutableRawPointer, UInt32, Int64) -> Void
    private typealias SetLocation = @convention(c) (UnsafeMutableRawPointer, Double, Double) -> Void
    private typealias SetAuthentication = @convention(c) (UnsafeMutableRawPointer, UnsafeMutableRawPointer) -> Void
    private typealias Factory = @convention(c) (UnsafeMutableRawPointer, Selector, UnsafeMutableRawPointer, Int32, UInt32) -> UnsafeMutableRawPointer?
    private typealias Connection = @convention(c) () -> UInt32
    private typealias WindowOwner = @convention(c) (UInt32, UInt32, UnsafeMutablePointer<UInt32>) -> Int32
    private typealias ConnectionPSN = @convention(c) (UInt32, UnsafeMutableRawPointer) -> Int32
    private typealias FocusRecord = @convention(c) (UnsafeRawPointer, UnsafePointer<UInt8>) -> Int32

    // Keep the handle open for the lifetime of all resolved function pointers.
    private static let library = dlopen("/System/Library/PrivateFrameworks/SkyLight.framework/SkyLight", RTLD_LAZY | RTLD_GLOBAL)
    private static func symbol<T>(_ name: String, _ type: T.Type) -> T? {
        guard library != nil, let address = dlsym(UnsafeMutableRawPointer(bitPattern: -2), name) else { return nil }
        return unsafeBitCast(address, to: type)
    }
    private static let post = symbol("SLEventPostToPid", Post.self)
    private static let setInteger = symbol("SLEventSetIntegerValueField", SetInteger.self)
    private static let setLocation = symbol("CGEventSetWindowLocation", SetLocation.self)
    private static let setAuthentication = symbol("SLEventSetAuthenticationMessage", SetAuthentication.self)
    private static let factory = symbol("objc_msgSend", Factory.self)
    private static let connection = symbol("CGSMainConnectionID", Connection.self)
    private static let windowOwner = symbol("SLSGetWindowOwner", WindowOwner.self)
    private static let connectionPSN = symbol("SLSGetConnectionPSN", ConnectionPSN.self)
    private static let focusRecord = symbol("SLPSPostEventRecordTo", FocusRecord.self)
    private static let authenticationSelector = NSSelectorFromString("messageWithEventRecord:pid:version:")
    private static var authenticationClass: AnyClass? { NSClassFromString("SLSEventAuthenticationMessage") }

    private static var keyboardLayoutSupported: Bool {
        #if arch(arm64)
        return ProcessInfo.processInfo.operatingSystemVersion.majorVersion == 26
        #else
        return false
        #endif
    }

    public static func available() -> [String: Any] {
        let mouse = post != nil && setInteger != nil && setLocation != nil && connection != nil
            && windowOwner != nil && connectionPSN != nil && focusRecord != nil
        let auth = authenticationClass.map { class_getClassMethod($0, authenticationSelector) != nil } ?? false
        return ["mouse_symbols_available": mouse,
                "keyboard_symbols_available": mouse && auth && factory != nil && setAuthentication != nil,
                "keyboard_layout_supported": keyboardLayoutSupported,
                "uses_private_api": true, "delivery_verified": false,
                "foreground_fallback": false]
    }

    /// Posts keyDown/keyUp pairs in order after one focus preparation.
    public static func postKeys(_ pairs: [(down: CGEvent, up: CGEvent)], pid: pid_t, windowID: UInt32) async throws {
        guard !pairs.isEmpty, pairs.allSatisfy({ $0.down.type == .keyDown && $0.up.type == .keyUp }) else {
            throw LocalCUError.invalidArgument("Background keyboard input requires keyDown/keyUp pairs")
        }
        _ = try validateTarget(pid: pid, windowID: windowID)
        guard let launchDate = ProcessIdentity.startDate(pid: pid) else { throw unavailable("Cannot bind keyboard target launch identity") }
        guard keyboardLayoutSupported, let cls = authenticationClass,
              class_getClassMethod(cls, authenticationSelector) != nil,
              let factory, let setAuthentication, let post, let setInteger else {
            throw unavailable("Authenticated keyboard ABI is unavailable on this OS/architecture")
        }
        // Prepare every envelope before changing any target state or sending a key.
        var envelopes: [AnyObject] = []
        try autoreleasepool {
            for event in pairs.flatMap({ [$0.down, $0.up] }) {
                let pointer = raw(event)
                guard let record = eventRecord(pointer) else { throw unavailable("Record unavailable") }
                guard let message = factory(Unmanaged.passUnretained(cls as AnyObject).toOpaque(),
                                            authenticationSelector, record, pid, 0) else {
                    throw unavailable("Could not construct the keyboard authentication envelope")
                }
                setInteger(pointer, 40, Int64(pid))
                for field: UInt32 in [51, 91, 92] { setInteger(pointer, field, Int64(windowID)) }
                setAuthentication(pointer, message)
                envelopes.append(Unmanaged<AnyObject>.fromOpaque(message).takeUnretainedValue())
            }
        }
        try Task.checkCancellation()
        try await prepare(pid: pid, windowID: windowID)
        try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
        var pending: CGEvent?
        defer {
            if let up = pending, (try? validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)) != nil { post(pid, raw(up)) }
            withExtendedLifetime(envelopes) {}
        }
        for (index, pair) in pairs.enumerated() {
            if index > 0 {
                try Task.checkCancellation()
                try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
            }
            post(pid, raw(pair.down)); pending = pair.up
            try await Task.sleep(nanoseconds: 10_000_000)
            try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
            post(pid, raw(pair.up)); pending = nil
        }
    }

    /// Caller supplies screen-space coordinates, already mapped from its snapshot.
    /// An off-screen Chromium activation primer is generated only for streams
    /// that press a button, a hover before the press included; scroll-only and
    /// move-only calls do not synthesize a click.
    public static func postMouse(events: [CGEvent], pid: pid_t, windowID: UInt32, delaysAfterMS: [Int]? = nil) async throws {
        guard !events.isEmpty, events.count <= 4096 else {
            throw LocalCUError.invalidArgument("Background mouse stream must contain 1...4096 events")
        }
        let bounds = try validateTarget(pid: pid, windowID: windowID)
        guard let launchDate = ProcessIdentity.startDate(pid: pid) else { throw unavailable("Cannot bind mouse target launch identity") }
        let allowed: Set<CGEventType> = [.mouseMoved, .leftMouseDown, .leftMouseUp, .leftMouseDragged,
                                        .rightMouseDown, .rightMouseUp, .rightMouseDragged,
                                        .otherMouseDown, .otherMouseUp, .otherMouseDragged, .scrollWheel]
        guard events.allSatisfy({ allowed.contains($0.type) && $0.location.x.isFinite && $0.location.y.isFinite
            && bounds.contains($0.location) }) else {
            throw LocalCUError.invalidArgument("Mouse events must be mouse/scroll events inside the exact target window")
        }
        let delays = delaysAfterMS ?? [Int](repeating: 5, count: events.count)
        guard delays.count == events.count, delays.allSatisfy({ (0...1000).contains($0) }),
              delays.reduce(0, +) <= 30_000 else {
            throw LocalCUError.invalidArgument("Mouse timing must match the stream, stay within 0...1000 ms per event and 30 seconds total")
        }
        var balance = Set<UInt32>()
        for event in events {
            if let release = releaseType(for: event.type) {
                guard balance.insert(release.rawValue).inserted else {
                    throw LocalCUError.invalidArgument("Mouse stream presses a button that is already down")
                }
            }
            else if [.leftMouseUp, .rightMouseUp, .otherMouseUp].contains(event.type) {
                guard balance.remove(event.type.rawValue) != nil else {
                    throw LocalCUError.invalidArgument("Mouse stream contains a release without a matching press")
                }
            }
        }
        guard balance.isEmpty else { throw LocalCUError.invalidArgument("Mouse stream must release every pressed button") }
        guard let post, let setInteger, let setLocation else { throw unavailable("Mouse event symbols are unavailable") }
        // Wheels use the authenticated event-record route as well. Prepare every
        // envelope before target focus changes; failure must not become an
        // unsigned retry that reports the same apparent success.
        var wheelEnvelopes: [AnyObject] = []
        for event in events where event.type == .scrollWheel {
            guard keyboardLayoutSupported, let cls = authenticationClass,
                  class_getClassMethod(cls, authenticationSelector) != nil,
                  let factory, let setAuthentication else {
                throw unavailable("Authenticated wheel ABI is unavailable on this OS/architecture")
            }
            event.timestamp = DispatchTime.now().uptimeNanoseconds
            event.setIntegerValueField(.eventSourceStateID, value: Int64(CGEventSourceStateID.hidSystemState.rawValue))
            let pointer = raw(event)
            setInteger(pointer, 40, Int64(pid))
            for field: UInt32 in [51, 91, 92] { setInteger(pointer, field, Int64(windowID)) }
            setLocation(pointer, event.location.x - bounds.minX, event.location.y - bounds.minY)
            guard let record = eventRecord(pointer),
                  let message = factory(Unmanaged.passUnretained(cls as AnyObject).toOpaque(),
                                        authenticationSelector, record, pid, 0) else {
                throw unavailable("Could not construct the wheel authentication envelope")
            }
            setAuthentication(pointer, message)
            wheelEnvelopes.append(Unmanaged<AnyObject>.fromOpaque(message).takeUnretainedValue())
        }
        defer { withExtendedLifetime(wheelEnvelopes) {} }
        let isDrag = events.contains { [.leftMouseDragged, .rightMouseDragged, .otherMouseDragged].contains($0.type) }
        let group = Int64(DispatchTime.now().uptimeNanoseconds & 0x7fff_ffff)
        func send(_ event: CGEvent, phase: Int64, subtype: Int64 = 3) {
            let pointer = raw(event)
            if event.type != .scrollWheel {
                setInteger(pointer, 0, phase)
                // Cua's background drag uses normal mouse subtype for the actual
                // gesture; only the activation primer uses touch subtype 3.
                setInteger(pointer, 7, subtype)
                setInteger(pointer, 58, group)
            }
            setInteger(pointer, 40, Int64(pid))
            for field: UInt32 in [51, 91, 92] { setInteger(pointer, field, Int64(windowID)) }
            if event.type == .scrollWheel {
                setLocation(pointer, event.location.x - bounds.minX, event.location.y - bounds.minY)
            } else {
                setLocation(pointer, event.location.x, event.location.y)
            }
            post(pid, pointer)
        }
        // Allocate the full primer before modifying target focus state.
        var primer: [CGEvent] = []
        if let first = events.first(where: { [.leftMouseDown, .rightMouseDown, .otherMouseDown].contains($0.type) }) {
            guard !bounds.contains(CGPoint(x: -1, y: -1)) else {
                throw unavailable("The Chromium primer point intersects this window; this display layout is unsupported")
            }
            guard let move = CGEvent(mouseEventSource: nil, mouseType: .mouseMoved, mouseCursorPosition: first.location, mouseButton: .left),
                  let down = CGEvent(mouseEventSource: nil, mouseType: .leftMouseDown, mouseCursorPosition: CGPoint(x: -1, y: -1), mouseButton: .left),
                  let up = CGEvent(mouseEventSource: nil, mouseType: .leftMouseUp, mouseCursorPosition: CGPoint(x: -1, y: -1), mouseButton: .left) else {
                throw unavailable("Could not construct the Chromium activation primer")
            }
            for event in [move, down, up] { event.flags = first.flags }
            down.setIntegerValueField(.mouseEventClickState, value: 1)
            up.setIntegerValueField(.mouseEventClickState, value: 1)
            primer = [move, down, up]
        }
        guard !Task.isCancelled else { throw LocalCUError.operation("Background input cancelled before delivery") }
        try await prepare(pid: pid, windowID: windowID)
        if events[0].type == .scrollWheel {
            guard let source = CGEventSource(stateID: .hidSystemState),
                  let move = CGEvent(mouseEventSource: source, mouseType: .mouseMoved,
                                     mouseCursorPosition: events[0].location, mouseButton: .left) else {
                throw unavailable("Could not construct the scroll tracking primer")
            }
            move.flags = events[0].flags
            try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
            send(move, phase: 2)
            try await Task.sleep(nanoseconds: 12_000_000)
        }
        if primer.count == 3 {
            try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
            send(primer[0], phase: 2)
            try await Task.sleep(nanoseconds: 15_000_000)
            try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
            send(primer[1], phase: 1)
            do {
                try await Task.sleep(nanoseconds: 1_000_000)
                try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
                send(primer[2], phase: 2)
            } catch {
                if (try? validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)) != nil { send(primer[2], phase: 2) }
                throw error
            }
            try await Task.sleep(nanoseconds: 100_000_000)
        }
        var pendingReleases: [UInt32: CGMouseButton] = [:]
        var last = events[0]
        defer {
            // No button remains held if the surrounding task is cancelled.
            for (rawType, button) in pendingReleases where
                (try? validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)) != nil {
                if let type = CGEventType(rawValue: rawType),
                   let release = CGEvent(mouseEventSource: nil, mouseType: type,
                                         mouseCursorPosition: last.location, mouseButton: button) {
                    release.flags = last.flags
                    send(release, phase: 3, subtype: isDrag ? 0 : 3)
                }
            }
        }
        for (index, event) in events.enumerated() {
            try Task.checkCancellation()
            let currentBounds = try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
            guard sameWindowFrame(bounds, currentBounds), currentBounds.contains(event.location) else { throw LocalCUError.staleSnapshot("Window moved during mouse input") }
            send(event, phase: 3, subtype: isDrag ? 0 : 3)
            last = event
            if let type = releaseType(for: event.type) {
                pendingReleases[type.rawValue] = CGMouseButton(rawValue: UInt32(event.getIntegerValueField(.mouseEventButtonNumber))) ?? .left
            } else { pendingReleases.removeValue(forKey: event.type.rawValue) }
            if delays[index] > 0 { try await Task.sleep(nanoseconds: UInt64(delays[index]) * 1_000_000) }
        }
    }

    private static func releaseType(for type: CGEventType) -> CGEventType? {
        switch type {
        case .leftMouseDown: return .leftMouseUp
        case .rightMouseDown: return .rightMouseUp
        case .otherMouseDown: return .otherMouseUp
        default: return nil
        }
    }

    private static func validateTarget(pid: pid_t, windowID: UInt32) throws -> CGRect {
        guard pid > 0, windowID > 0, CGPreflightPostEventAccess() else {
            throw LocalCUError.permission("Background input requires Post Events permission and a valid target")
        }
        guard let window = WindowService.window(id: windowID, pid: pid) else {
            throw LocalCUError.target("Background window no longer exists or belongs to a different process")
        }
        // Events addressed to a window are delivered through the window server,
        // which was only ever asked about windows on screen here.
        guard window.onScreen else {
            throw LocalCUError.target("The target window is not on screen (minimized, hidden, behind a full-screen app or on another Space), so background mouse, key and scroll input cannot reach it; an indexed accessibility action still can")
        }
        let bounds = window.rectangle
        guard !bounds.isEmpty, bounds.minX.isFinite, bounds.minY.isFinite, bounds.width.isFinite, bounds.height.isFinite else {
            throw LocalCUError.target("Background window no longer exists or belongs to a different process")
        }
        return bounds
    }

    /// Returns the window's current bounds.
    @discardableResult
    private static func validateIdentity(pid: pid_t, windowID: UInt32, launchDate: Date) throws -> CGRect {
        guard let app = NSRunningApplication(processIdentifier: pid), !app.isTerminated, ProcessIdentity.startDate(pid: pid) == launchDate else {
            throw LocalCUError.staleSnapshot("Target process exited or launch identity changed during input")
        }
        return try validateTarget(pid: pid, windowID: windowID)
    }

    /// Internal menu preparation shares the exact-window focus recipe without
    /// manufacturing a keyboard event. Caller must separately prove the menu's
    /// focused-window or single-document authority before and after this await.
    static func prepareWindowFocus(pid: pid_t, windowID: UInt32) async throws {
        guard let launchDate = ProcessIdentity.startDate(pid: pid) else { throw unavailable("Cannot bind menu target launch identity") }
        try await prepare(pid: pid, windowID: windowID)
        try validateIdentity(pid: pid, windowID: windowID, launchDate: launchDate)
    }

    private static func prepare(pid: pid_t, windowID: UInt32) async throws {
        _ = try validateTarget(pid: pid, windowID: windowID)
        guard let connection, let windowOwner, let connectionPSN, let focusRecord else {
            throw unavailable("Exact-window focus-record symbols are unavailable")
        }
        var owner: UInt32 = 0
        guard windowOwner(connection(), windowID, &owner) == 0, owner != 0 else {
            throw unavailable("WindowServer could not resolve the exact window owner")
        }
        var psn: UInt64 = 0
        guard connectionPSN(owner, &psn) == 0 else { throw unavailable("WindowServer could not resolve the owner PSN") }
        var record = [UInt8](repeating: 0, count: 0xf8)
        record[0x04] = 0xf8
        record[0x08] = 0x0d
        for byte in 0..<4 { record[0x3c + byte] = UInt8(truncatingIfNeeded: windowID >> (byte * 8)) }
        record[0x8a] = 0x01
        // Target-only focus record. Never defocus the user's front process,
        // activate an NSRunningApplication, raise a window, or warp the cursor.
        let status = record.withUnsafeBufferPointer { buffer in
            withUnsafePointer(to: &psn) { focusRecord(UnsafeRawPointer($0), buffer.baseAddress!) }
        }
        guard status == 0 else { throw unavailable("WindowServer rejected the focus record (status \(status))") }
        try await Task.sleep(nanoseconds: 50_000_000)
    }

    private static func raw(_ event: CGEvent) -> UnsafeMutableRawPointer {
        Unmanaged.passUnretained(event).toOpaque()
    }

    /// The pinned Cua source documents the arm64 __CGEvent layout. Accept its
    /// single known offset, never scan guessed offsets. Read through Mach so a
    /// missing/unmapped slot fails instead of dereferencing an invalid pointer.
    private static func eventRecord(_ event: UnsafeMutableRawPointer) -> UnsafeMutableRawPointer? {
        var address: UInt64 = 0
        var read: mach_vm_size_t = 0
        let status = withUnsafeMutablePointer(to: &address) {
            mach_vm_read_overwrite(mach_task_self_, mach_vm_address_t(UInt(bitPattern: event)) + 24,
                                   8, mach_vm_address_t(UInt(bitPattern: $0)), &read)
        }
        guard status == KERN_SUCCESS, read == 8, address > 4096, address % 8 == 0,
              let record = UnsafeMutableRawPointer(bitPattern: UInt(address)) else { return nil }
        var probe: UInt64 = 0
        let probeStatus = withUnsafeMutablePointer(to: &probe) {
            mach_vm_read_overwrite(mach_task_self_, address, 8, mach_vm_address_t(UInt(bitPattern: $0)), &read)
        }
        return probeStatus == KERN_SUCCESS && read == 8 ? record : nil
    }

    private static func unavailable(_ reason: String) -> LocalCUError {
        .operation("background_input_unavailable: \(reason)")
    }
}
