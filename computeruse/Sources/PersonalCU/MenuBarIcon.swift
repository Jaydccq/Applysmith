import AppKit

@MainActor
enum MenuBarIcon {
    private static let base: NSImage = {
        guard let url = Bundle.module.url(forResource: "menu-bar", withExtension: "png"),
              let image = NSImage(contentsOf: url) else { preconditionFailure("Missing menu bar artwork") }
        image.size = NSSize(width: 18, height: 18)
        return image
    }()

    static func image(badge: String?, description: String) -> NSImage {
        let image = NSImage(size: NSSize(width: 20, height: 20), flipped: false) { bounds in
            base.draw(in: bounds.insetBy(dx: 1, dy: 1))
            if let badge, let symbol = NSImage(systemSymbolName: badge, accessibilityDescription: nil) {
                let badgeRect = NSRect(x: 11, y: 0, width: 9, height: 9)
                NSGraphicsContext.current?.cgContext.clear(badgeRect.insetBy(dx: -1, dy: -1))
                symbol.draw(in: badgeRect)
            }
            return true
        }
        image.isTemplate = true
        image.accessibilityDescription = description
        return image
    }
}
