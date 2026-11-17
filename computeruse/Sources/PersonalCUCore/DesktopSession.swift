import CoreGraphics
import Foundation

public enum DesktopSession {
    private static var info: [String: Any]? { CGSessionCopyCurrentDictionary() as? [String: Any] }

    public static var locked: Bool? { info?["CGSSessionScreenIsLocked"] as? Bool }

    public static var status: [String: Any] {
        guard let info else { return ["available": false] }
        return ["available": true,
                "locked": info["CGSSessionScreenIsLocked"] as? Bool ?? false,
                "on_console": info["kCGSSessionOnConsoleKey"] as? Bool ?? false]
    }

    static func requireUnlockedActivation() throws {
        if locked == true {
            throw LocalCUError.permission("The macOS desktop is locked. Unlock it locally before requesting activation.")
        }
    }
}
