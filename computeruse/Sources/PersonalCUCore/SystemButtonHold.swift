import AppKit
import CoreGraphics

/// Makes the system report the left mouse button as pressed for the length of
/// a drag in a Chromium window. Chromium builds a mouse event's `buttons` from
/// the system's pressed buttons, which a drag addressed to one window never
/// sets, so a page that tests `event.buttons` saw no drag.
///
/// A left-button press is posted at the user's pointer and removed by this
/// process's own event tap before any window gets it: the system counts the
/// button as pressed, the pointer does not move and nothing is clicked
/// (measured 2026-11-16, docs/architecture/computer-use.md). The press is
/// posted only after the tap has removed a probe, the release of a button no
/// mouse has, so a tap that is not working costs no click at the user's
/// pointer; and never while the user holds the real button. The tap is
/// enabled only from the probe until it has removed the release, so the user's
/// own clicks do not pass through this process at any other time. A program
/// that taps events before the annotated session tap still sees the press.
///
/// A process that dies holding the button cannot release it, so a second
/// process, the guard (`guardMain`, the executable's `button-guard` command),
/// is told of each press and release over a pipe and posts the release when
/// the pipe closes while the button is held.
@MainActor
public enum SystemButtonHold {
    private static let tag: Int64 = 0x5043_5548
    private struct Tap { var removed = 0; var port: CFMachPort? }
    private static let tap = { let tap = UnsafeMutablePointer<Tap>.allocate(capacity: 1); tap.initialize(to: Tap()); return tap }()

    /// Nil when the system now reports the left button as pressed because of
    /// this call, and the caller then owes one `release()`; otherwise why not.
    static func hold() async -> String? {
        guard NSEvent.pressedMouseButtons & 1 == 0 else { return "the user holds the real mouse button" }
        guard let port = installedTap() else { return "the event tap could not be created" }
        CGEvent.tapEnable(tap: port, enable: true)
        guard await removes(.otherMouseUp, button: 31, until: { true }) else {
            CGEvent.tapEnable(tap: port, enable: false)
            return "the event tap did not answer"
        }
        guard NSEvent.pressedMouseButtons & 1 == 0 else {
            CGEvent.tapEnable(tap: port, enable: false)
            return "the user holds the real mouse button"
        }
        tell("h")
        if await removes(.leftMouseDown, button: 0, until: { NSEvent.pressedMouseButtons & 1 == 1 }) { return nil }
        release()
        return "the system did not report the press"
    }

    /// The tap switches itself off as it removes this release.
    static func release() {
        _ = post(.leftMouseUp, button: 0)
        tell("r")
    }

    private static var guardInput: FileHandle?

    /// Tells the guard, started on first use, of a press ("h") or a release
    /// ("r"). Without a guard the hold goes on: only a death in mid-drag is
    /// then uncovered.
    private static func tell(_ what: String) {
        if guardInput == nil, let executable = Bundle.main.executableURL {
            signal(SIGPIPE, SIG_IGN)
            let process = Process(), pipe = Pipe()
            process.executableURL = executable
            process.arguments = ["button-guard"]
            process.standardInput = pipe
            process.standardOutput = FileHandle.nullDevice
            process.standardError = FileHandle.nullDevice
            if (try? process.run()) != nil { guardInput = pipe.fileHandleForWriting }
        }
        if (try? guardInput?.write(contentsOf: Data(what.utf8))) == nil { guardInput = nil }
    }

    /// The guard process: reads "h" and "r" until its input closes, which is
    /// when the service has exited, and releases the button if the last word
    /// was a press. Nothing removes this release: a window under the pointer
    /// gets a mouse-up with no mouse-down before it.
    nonisolated public static func guardMain() {
        var held = false
        while let byte = try? FileHandle.standardInput.read(upToCount: 1), let word = byte.first {
            held = word == UInt8(ascii: "h")
        }
        guard held, let at = CGEvent(source: nil)?.location,
              let up = CGEvent(mouseEventSource: CGEventSource(stateID: .hidSystemState), mouseType: .leftMouseUp, mouseCursorPosition: at, mouseButton: .left) else { return }
        up.post(tap: .cghidEventTap)
        usleep(200_000)
    }

    /// Posts one tagged event and waits, up to 0.2 s, for the tap to remove it.
    private static func removes(_ type: CGEventType, button: Int64, until done: () -> Bool) async -> Bool {
        let before = tap.pointee.removed
        guard post(type, button: button) else { return false }
        for _ in 0..<20 where !(tap.pointee.removed > before && done()) {
            do { try await Task.sleep(nanoseconds: 10_000_000) } catch { break }
        }
        return tap.pointee.removed > before && done()
    }

    private static func post(_ type: CGEventType, button: Int64) -> Bool {
        guard let at = CGEvent(source: nil)?.location,
              let event = CGEvent(mouseEventSource: CGEventSource(stateID: .hidSystemState), mouseType: type, mouseCursorPosition: at,
                                  mouseButton: button == 0 ? .left : .center) else { return false }
        event.setIntegerValueField(.mouseEventButtonNumber, value: button)
        event.setIntegerValueField(.eventSourceUserData, value: tag)
        event.post(tap: .cghidEventTap)
        return true
    }

    /// At the annotated session tap the system has already counted the press;
    /// a tap earlier in the path removes it before it is counted.
    private static func installedTap() -> CFMachPort? {
        if let port = tap.pointee.port { return port }
        let mask = [CGEventType.leftMouseDown, .leftMouseUp, .otherMouseUp].reduce(CGEventMask(0)) { $0 | CGEventMask(1 << $1.rawValue) }
        let callback: CGEventTapCallBack = { _, type, event, state in
            guard [.leftMouseDown, .leftMouseUp, .otherMouseUp].contains(type), let tap = state?.assumingMemoryBound(to: Tap.self),
                  event.getIntegerValueField(.eventSourceUserData) == 0x5043_5548 else { return Unmanaged.passUnretained(event) }
            tap.pointee.removed += 1
            if type == .leftMouseUp, let port = tap.pointee.port { CGEvent.tapEnable(tap: port, enable: false) }
            return nil
        }
        guard let created = CGEvent.tapCreate(tap: .cgAnnotatedSessionEventTap, place: .headInsertEventTap, options: .defaultTap,
                                              eventsOfInterest: mask, callback: callback, userInfo: tap) else { return nil }
        CGEvent.tapEnable(tap: created, enable: false)
        CFRunLoopAddSource(CFRunLoopGetMain(), CFMachPortCreateRunLoopSource(nil, created, 0), .commonModes)
        tap.pointee.port = created
        return created
    }
}
