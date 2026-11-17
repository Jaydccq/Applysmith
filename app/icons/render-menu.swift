import AppKit

let root = URL(fileURLWithPath: #filePath)
    .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
let artwork = [
    ("app/icons/menu-bar.svg", "app/icons/menu-bar.png"),
    ("computeruse/icons/menu-bar.svg", "computeruse/Sources/PersonalCU/Resources/menu-bar.png")
]
for (source, destination) in artwork {
    guard let image = NSImage(contentsOf: root.appendingPathComponent(source)),
          let bitmap = NSBitmapImageRep(bitmapDataPlanes: nil, pixelsWide: 44, pixelsHigh: 44,
                                        bitsPerSample: 8, samplesPerPixel: 4, hasAlpha: true,
                                        isPlanar: false, colorSpaceName: .deviceRGB, bytesPerRow: 0, bitsPerPixel: 0)
    else { fatalError("Cannot render \(source)") }
    bitmap.size = NSSize(width: 22, height: 22)
    guard let context = NSGraphicsContext(bitmapImageRep: bitmap) else { fatalError("Cannot create bitmap context") }
    NSGraphicsContext.saveGraphicsState()
    NSGraphicsContext.current = context
    image.draw(in: NSRect(x: 0, y: 0, width: 22, height: 22))
    NSGraphicsContext.restoreGraphicsState()
    guard let png = bitmap.representation(using: .png, properties: [:]) else { fatalError("Cannot encode \(source)") }
    try png.write(to: root.appendingPathComponent(destination))
    print("Rendered \(destination)")
}
