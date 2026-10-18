// swift-tools-version:5.9
// For building and testing from sdks/ios. Apps depend on the repository URL, which resolves the
// root Package.swift; keep the platforms, products and targets of the two files in step.
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
