import Foundation

/// What the manager window, the menu and the menu bar icon say about the
/// service, from the same facts the doctor tool reports.
public struct ServiceSummary: Equatable {
    public enum Level: Equatable { case ready, attention, stopped }

    public struct Facts {
        public var socketReady = true
        public var paused = false
        public var backgroundEnabled = true
        public var accessibility = true
        public var screenRecording = true
        public var locked = false
        public init() {}
    }

    public let level: Level
    public let headline: String
    /// What to do about it; empty when nothing is needed.
    public let advice: String

    /// The first thing that keeps a client from acting, in the order a client
    /// meets them: no service, a missing permission, a locked desktop, the
    /// pause, background control switched off.
    public init(_ f: Facts) {
        let missing = [f.accessibility ? nil : "Accessibility", f.screenRecording ? nil : "Screen Recording"].compactMap { $0 }
        if !f.socketReady {
            (level, headline, advice) = (.stopped, "Not running", "The local service did not start. Quit PersonalCU and open it again.")
        } else if !missing.isEmpty {
            (level, headline) = (.attention, "Needs \(missing.joined(separator: " and "))")
            advice = "Grant it in System Settings, Privacy & Security, then quit and reopen PersonalCU."
        } else if f.locked {
            (level, headline, advice) = (.attention, "Desktop locked", "Unlock this Mac so clients can read and act in windows.")
        } else if f.paused {
            (level, headline, advice) = (.attention, "Paused", "Clients can still read windows; every click, key and scroll is refused until you resume.")
        } else if !f.backgroundEnabled {
            (level, headline, advice) = (.attention, "Background control off", "Clients can act only in the frontmost window, and only when they ask for foreground input.")
        } else {
            (level, headline, advice) = (.ready, "Ready", "")
        }
    }
}

/// The latest actions clients ran, newest first: when, which tool and in
/// which app. It never holds an argument, so no typed text, key or point.
public struct ActionLog {
    public struct Entry: Equatable {
        public let at: Date
        public let tool: String
        public let app: String?
        public init(at: Date, tool: String, app: String?) { (self.at, self.tool, self.app) = (at, tool, app) }
    }

    public private(set) var entries: [Entry] = []
    public let capacity: Int
    public init(capacity: Int) { self.capacity = max(capacity, 1) }

    public mutating func record(_ tool: String, app: String?, at: Date = Date()) {
        entries.insert(Entry(at: at, tool: tool, app: app), at: 0)
        if entries.count > capacity { entries.removeLast(entries.count - capacity) }
    }
}
