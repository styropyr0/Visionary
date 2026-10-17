// swift-tools-version:5.9
import PackageDescription

let package = Package(
    name: "Visionary",
    platforms: [.iOS(.v14), .macOS(.v11)],
    products: [
        .library(name: "Visionary", targets: ["Visionary"]),
    ],
    targets: [
        .target(name: "Visionary", path: "Sources/Visionary"),
    ]
)
