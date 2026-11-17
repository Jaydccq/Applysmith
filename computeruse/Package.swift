// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "PersonalCU",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "personal-cu", targets: ["PersonalCU"])],
    dependencies: [
        .package(url: "https://github.com/modelcontextprotocol/swift-sdk.git", exact: "0.11.0")
    ],
    targets: [
        .target(name: "PersonalCUCore"),
        .executableTarget(name: "PersonalCU", dependencies: ["PersonalCUCore", .product(name: "MCP", package: "swift-sdk")], resources: [.copy("Resources/js-worker.mjs"), .copy("Resources/menu-bar.png")]),
        .testTarget(name: "PersonalCUCoreTests", dependencies: ["PersonalCUCore"])
    ],
    swiftLanguageModes: [.v5]
)
