// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "ATCKit",
    platforms: [
        .macOS(.v26),
    ],
    products: [
        .library(name: "ATCAPI", targets: ["ATCAPI"]),
    ],
    targets: [
        .target(name: "ATCAPI"),
        .testTarget(name: "ATCAPITests", dependencies: ["ATCAPI"]),
    ]
)
