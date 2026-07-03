// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "CockpitKit",
    platforms: [
        .macOS(.v15),
    ],
    products: [
        .library(name: "CockpitAPI", targets: ["CockpitAPI"]),
    ],
    targets: [
        .target(name: "CockpitAPI"),
        .testTarget(name: "CockpitAPITests", dependencies: ["CockpitAPI"]),
    ]
)
