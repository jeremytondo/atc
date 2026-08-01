// ATCAppServerAPI: the typed client for the ATC App Server HTTP contract.
//
// `Client`, `Operations`, and `Components` are generated at build time by the
// Swift OpenAPI Generator plugin from `openapi.json` in this directory — a
// symlink to the canonical checked-in artifact at `app-server/openapi.json`,
// which is itself generated from the App Server's `HttpApi` contract module
// (`mise run app-server:openapi`). Nothing in this target is written by hand
// except this file.
//
// Construct a client with the URLSession transport:
//
//     import OpenAPIURLSession
//
//     let client = Client(
//         serverURL: try Servers.Server1.url(),
//         transport: URLSessionTransport()
//     )
//     let health = try await client.getHealth().ok.body.json
