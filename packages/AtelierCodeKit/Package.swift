// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "AtelierCodeKit",
    platforms: [
        .macOS(.v15),
    ],
    products: [
        .library(name: "AtelierCodeAPI", targets: ["AtelierCodeAPI"]),
    ],
    targets: [
        .target(name: "AtelierCodeAPI"),
        .testTarget(name: "AtelierCodeAPITests", dependencies: ["AtelierCodeAPI"]),
    ]
)
