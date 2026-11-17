import Foundation
import CoreGraphics
import Darwin

public struct DesktopResult {
    public var data: [String: Any]
    public var png: Data?
    public var images: [Data]

    public init(data: [String: Any], png: Data? = nil, images: [Data] = []) {
        self.data = data
        self.png = png
        self.images = images
    }
}

public enum LocalCUError: LocalizedError {
    case invalidArgument(String)
    case permission(String)
    case target(String)
    case staleSnapshot(String)
    case accessibility(String, Int32)
    case operation(String)

    public var errorDescription: String? {
        switch self {
        case .invalidArgument(let detail): return "invalid_argument: \(detail)"
        case .permission(let detail): return "permission_required: \(detail)"
        case .target(let detail): return "target_error: \(detail)"
        case .staleSnapshot(let detail): return "stale_snapshot: \(detail)"
        case .accessibility(let detail, let code): return "accessibility_error: \(detail) (AXError \(code))"
        case .operation(let detail): return "operation_failed: \(detail)"
        }
    }
}

/// ScreenCaptureKit window frames and AX window positions use global, top-left points.
/// The image has no window shadow, and its actual encoded dimensions define scaling.
struct ScreenshotGeometry {
    let windowRect: CGRect
    let pixelWidth: Int
    let pixelHeight: Int

    func globalPoint(x: Double, y: Double) throws -> CGPoint {
        guard x.isFinite, y.isFinite, pixelWidth > 0, pixelHeight > 0,
              windowRect.minX.isFinite, windowRect.minY.isFinite, windowRect.width.isFinite, windowRect.height.isFinite,
              windowRect.width > 0, windowRect.height > 0,
              x >= 0, y >= 0, x < Double(pixelWidth), y < Double(pixelHeight) else {
            throw LocalCUError.invalidArgument("x and y must be within this snapshot's screenshot pixel bounds")
        }
        return CGPoint(x: windowRect.minX + x * windowRect.width / Double(pixelWidth),
                       y: windowRect.minY + y * windowRect.height / Double(pixelHeight))
    }
}

/// Kernel start time identifies a process even when it was launched outside LaunchServices.
public enum ProcessIdentity {
    public static func startDate(pid: pid_t) -> Date? {
        guard pid > 0 else { return nil }
        var info = proc_bsdinfo()
        let size = MemoryLayout<proc_bsdinfo>.size
        let count = withUnsafeMutablePointer(to: &info) {
            proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, $0, Int32(size))
        }
        guard count == Int32(size), info.pbi_start_tvsec > 0, info.pbi_start_tvusec < 1_000_000 else { return nil }
        return Date(timeIntervalSince1970: Double(info.pbi_start_tvsec) + Double(info.pbi_start_tvusec) / 1_000_000)
    }
}

struct SnapshotIdentity {
    let pid: pid_t
    let launchDate: Date
    let created: TimeInterval

    func validate(requestedPID: Int, currentLaunchDate: Date?, terminated: Bool,
                  now: TimeInterval, lifetime: TimeInterval = 30) throws {
        guard requestedPID == Int(pid) else { throw LocalCUError.staleSnapshot("pid does not match snapshot target") }
        let age = now - created
        guard age >= 0, age < lifetime, !terminated, currentLaunchDate == launchDate else {
            throw LocalCUError.staleSnapshot("Snapshot expired or target process changed; request get_app_state")
        }
    }
}

func rectangleData(_ rect: CGRect) -> [String: Double] {
    ["x": rect.minX, "y": rect.minY, "width": rect.width, "height": rect.height]
}

/// Whether a window is where a snapshot saw it. The window server reports a
/// window's bounds one point larger on every side while the window is key than
/// while it is not (observed on macOS 26: 520 by 590 at 475,254 against 522 by
/// 592 at 474,253), so a window that only gained or lost the keyboard focus
/// has not moved: its content is where it was. The two axes do not change at
/// the same instant (observed: 520 by 592 at 475,253 in between), so each is
/// judged alone: unchanged, or grown or shrunk by one border about its centre.
/// Any other change is a move.
func sameWindowFrame(_ lhs: CGRect, _ rhs: CGRect) -> Bool {
    func sameAxis(_ origin: Double, _ size: Double) -> Bool {
        (abs(origin) <= 0.5 && abs(size) <= 0.5) || (abs(origin) <= 1 && abs(size + 2 * origin) <= 0.5)
    }
    return sameAxis(rhs.minX - lhs.minX, rhs.width - lhs.width) && sameAxis(rhs.minY - lhs.minY, rhs.height - lhs.height)
}

func sameRectangle(_ lhs: CGRect, _ rhs: CGRect, tolerance: Double = 0.5) -> Bool {
    abs(lhs.minX - rhs.minX) <= tolerance && abs(lhs.minY - rhs.minY) <= tolerance &&
    abs(lhs.width - rhs.width) <= tolerance && abs(lhs.height - rhs.height) <= tolerance
}
