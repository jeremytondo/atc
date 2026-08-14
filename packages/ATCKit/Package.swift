// swift-tools-version: 6.2
import PackageDescription

let package = Package(
    name: "ATCKit",
    platforms: [
        .macOS(.v26)
    ],
    products: [
        // Client generated from the App Server OpenAPI contract
        // (app-server/openapi.json).
        .library(name: "ATCAppServerAPI", targets: ["ATCAppServerAPI"]),
        // Hand-written App Server transports the OpenAPI document cannot
        // express: the terminal-attach WebSocket and the SSE event stream.
        .library(name: "ATCAppServerTransport", targets: ["ATCAppServerTransport"]),
    ],
    dependencies: [
        // Exact pins: generated code and the contract artifact are coupled,
        // so generator upgrades must be deliberate (see AGENTS.md).
        .package(url: "https://github.com/apple/swift-openapi-generator", exact: "1.13.0"),
        .package(url: "https://github.com/apple/swift-openapi-runtime", exact: "1.12.0"),
        .package(url: "https://github.com/apple/swift-openapi-urlsession", exact: "1.3.1"),
        .package(url: "https://github.com/apple/swift-http-types", exact: "1.6.0"),
    ],
    targets: [
        .target(
            name: "ATCAppServerAPI",
            dependencies: [
                .product(name: "OpenAPIRuntime", package: "swift-openapi-runtime"),
                .product(name: "OpenAPIURLSession", package: "swift-openapi-urlsession"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
            ],
            plugins: [
                .plugin(name: "OpenAPIGenerator", package: "swift-openapi-generator")
            ]
        ),
        .target(
            name: "ATCAppServerTransport",
            dependencies: ["ATCAppServerAPI"]
        ),
        .testTarget(
            name: "ATCAppServerTransportTests",
            dependencies: ["ATCAppServerTransport"]
        ),
        // The test's stub transport works with OpenAPIRuntime/HTTPTypes
        // values directly, so those imports are declared, not left to
        // transitive module leakage.
        .testTarget(
            name: "ATCAppServerAPITests",
            dependencies: [
                "ATCAppServerAPI",
                .product(name: "OpenAPIRuntime", package: "swift-openapi-runtime"),
                .product(name: "HTTPTypes", package: "swift-http-types"),
            ]
        ),
    ]
)
