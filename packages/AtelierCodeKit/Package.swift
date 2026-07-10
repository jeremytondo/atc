// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "AtelierCodeKit",
    platforms: [
        .macOS(.v26),
    ],
    products: [
        .library(name: "AtelierCodeAPI", targets: ["AtelierCodeAPI"]),
    ],
    targets: [
        .target(name: "AtelierCodeAPI"),
        .testTarget(name: "AtelierCodeAPITests", dependencies: ["AtelierCodeAPI"]),
    ]
)
