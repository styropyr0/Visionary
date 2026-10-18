// swift-tools-version:5.9
//
// Swift Package Manager entry point for the iOS SDK. SwiftPM only reads a manifest at the root of
// a repository, so this file lets apps depend on the repository URL directly:
//
//     .package(url: "https://github.com/styropyr0/Visionary.git", from: "0.4.0")
//
// It describes the same package as sdks/ios/Package.swift (kept for working on the SDK from that
// folder); keep the platforms, products and targets of the two files in step.
import PackageDescription

let package = Package(
    name: "Visionary",
    platforms: [.iOS(.v14), .macOS(.v11)],
    products: [
        .library(name: "Visionary", targets: ["Visionary"]),
    ],
    targets: [
        .target(name: "Visionary", path: "sdks/ios/Sources/Visionary"),
    ]
)
