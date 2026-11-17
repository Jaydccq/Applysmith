import AppKit
import ApplicationServices
import CoreGraphics
import Foundation

/// Bounded, process-scoped diagnostics. Never inspects event Unicode text or
/// clipboard contents, modifies an event, or posts synthetic input.
@MainActor
public enum DebugTap {
    /// One event's record. A key event keeps only its type and time: its key
    /// code would spell out what was typed, a password included.
    nonisolated static func record(type: CGEventType, event: CGEvent) -> [String: Any] {
        var record: [String: Any] = ["type": String(describing: type), "type_code": Int(type.rawValue),
                                     "time_seconds": Double(event.timestamp) / 1_000_000_000]
        if ![.keyDown, .keyUp, .flagsChanged].contains(type) {
            record["x"] = event.location.x
            record["y"] = event.location.y
        }
        return record
    }

    public static func capture(pid: pid_t, seconds: Double) async throws -> DesktopResult {
        guard pid > 0, seconds.isFinite, (0.1...10).contains(seconds) else {
            throw LocalCUError.invalidArgument("debug_tap requires a positive pid and seconds between 0.1 and 10")
        }
        guard CGPreflightListenEventAccess(), AXIsProcessTrusted() else {
            throw LocalCUError.permission("debug_tap requires Input Monitoring and Accessibility access")
        }
        guard let application = NSRunningApplication(processIdentifier: pid), !application.isTerminated,
              let launchDate = ProcessIdentity.startDate(pid: pid) else {
            throw LocalCUError.target("debug_tap target is not a running desktop application")
        }
        try Task.checkCancellation()
        let state = CaptureState()
        let types: [CGEventType] = [.keyDown, .keyUp, .flagsChanged, .mouseMoved,
                                    .leftMouseDown, .leftMouseUp, .leftMouseDragged,
                                    .rightMouseDown, .rightMouseUp, .rightMouseDragged,
                                    .otherMouseDown, .otherMouseUp, .otherMouseDragged,
                                    .scrollWheel]
        let mask = types.reduce(CGEventMask(0)) { $0 | (CGEventMask(1) << $1.rawValue) }
        let context = Unmanaged.passUnretained(state).toOpaque()
        guard let tap = CGEvent.tapCreateForPid(pid: pid, place: .headInsertEventTap,
                                              options: .listenOnly, eventsOfInterest: mask,
                                              callback: { _, type, event, context in
            if let context {
                Unmanaged<CaptureState>.fromOpaque(context).takeUnretainedValue().append(type: type, event: event)
            }
            return Unmanaged.passUnretained(event)
        }, userInfo: context) else {
            throw LocalCUError.operation("Could not create a listen-only event tap for the requested process")
        }
        guard let source = CFMachPortCreateRunLoopSource(kCFAllocatorDefault, tap, 0) else {
            CFMachPortInvalidate(tap)
            throw LocalCUError.operation("Could not create the diagnostic tap's run-loop source")
        }
        let runLoop = CFRunLoopGetMain()
        CFRunLoopAddSource(runLoop, source, .commonModes)
        CGEvent.tapEnable(tap: tap, enable: true)
        defer {
            CGEvent.tapEnable(tap: tap, enable: false)
            CFRunLoopRemoveSource(runLoop, source, .commonModes)
            CFMachPortInvalidate(tap)
            // The unmanaged callback pointer remains alive until the tap is gone.
            withExtendedLifetime(state) {}
        }
        let start = DispatchTime.now().uptimeNanoseconds
        let duration = UInt64(seconds * 1_000_000_000)
        while DispatchTime.now().uptimeNanoseconds - start < duration {
            try Task.checkCancellation()
            guard let current = NSRunningApplication(processIdentifier: pid), !current.isTerminated,
                  ProcessIdentity.startDate(pid: pid) == launchDate else {
                throw LocalCUError.target("debug_tap target exited or its process identity changed")
            }
            if state.disabled { break }
            let elapsed = DispatchTime.now().uptimeNanoseconds - start
            if elapsed >= duration { break }
            try await Task.sleep(nanoseconds: min(100_000_000, duration - elapsed))
        }
        try Task.checkCancellation()
        let summary = state.summary()
        return DesktopResult(data: ["pid": Int(pid), "seconds_requested": seconds,
                                    "seconds_observed": Double(DispatchTime.now().uptimeNanoseconds - start) / 1_000_000_000,
                                    "scope": "target_process", "listen_only": true,
                                    "events": summary.events, "observed_count": summary.count,
                                    "truncated": summary.truncated, "tap_disabled": summary.disabled,
                                    "max_records": 500, "text_recorded": false])
    }
}

/// The tap runs on the main run loop; a lock also protects the C callback's
/// nonisolated access and makes its lifetime independent of actor assumptions.
private final class CaptureState: @unchecked Sendable {
    private let lock = NSLock()
    private var events: [[String: Any]] = []
    private var count = 0
    private var wasDisabled = false

    var disabled: Bool {
        lock.lock()
        defer { lock.unlock() }
        return wasDisabled
    }

    func append(type: CGEventType, event: CGEvent) {
        lock.lock()
        defer { lock.unlock() }
        if type == .tapDisabledByTimeout || type == .tapDisabledByUserInput {
            wasDisabled = true
            return
        }
        count += 1
        guard events.count < 500 else { return }
        events.append(DebugTap.record(type: type, event: event))
    }

    func summary() -> (events: [[String: Any]], count: Int, truncated: Bool, disabled: Bool) {
        lock.lock()
        defer { lock.unlock() }
        return (events, count, count > events.count, wasDisabled)
    }
}
