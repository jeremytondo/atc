import Foundation
import Testing
@testable import ATC

/// Pure URL validation/normalization and duplicate rules for Connections.
@Suite("ConnectionURL")
struct ConnectionURLTests {
    // MARK: Scheme inference / persistence

    @Test("no scheme infers http and persists it explicitly")
    func inferScheme() {
        #expect(ConnectionURL.normalize("host:7331") == "http://host:7331")
        #expect(ConnectionURL.normalize("host") == "http://host")
    }

    @Test("explicit https scheme is preserved")
    func preservesHTTPS() {
        #expect(ConnectionURL.normalize("https://host") == "https://host")
        #expect(ConnectionURL.normalize("http://host:7331") == "http://host:7331")
    }

    // MARK: Rejections

    @Test("non-http(s) scheme is rejected")
    func rejectsBadScheme() {
        #expect(ConnectionURL.parse("ftp://host") == nil)
        #expect(ConnectionURL.parse("ws://host") == nil)
    }

    @Test("missing host is rejected")
    func rejectsMissingHost() {
        #expect(ConnectionURL.parse("http://") == nil)
        #expect(ConnectionURL.parse("") == nil)
        #expect(ConnectionURL.parse("   ") == nil)
    }

    @Test("a path other than a bare trailing slash is rejected")
    func rejectsPath() {
        #expect(ConnectionURL.parse("http://h/x") == nil)
        #expect(ConnectionURL.parse("http://h/x/y") == nil)
    }

    @Test("query is rejected")
    func rejectsQuery() {
        #expect(ConnectionURL.parse("http://h?a=b") == nil)
    }

    @Test("fragment is rejected")
    func rejectsFragment() {
        #expect(ConnectionURL.parse("http://h#frag") == nil)
    }

    @Test("userinfo is rejected")
    func rejectsUserinfo() {
        #expect(ConnectionURL.parse("http://user@h") == nil)
        #expect(ConnectionURL.parse("http://user:pass@h") == nil)
    }

    // MARK: Normalization

    @Test("a single trailing slash is stripped")
    func stripsTrailingSlash() {
        #expect(ConnectionURL.normalize("http://h:1/") == "http://h:1")
        #expect(ConnectionURL.normalize("http://h/") == "http://h")
    }

    @Test("surrounding whitespace is trimmed")
    func trimsWhitespace() {
        #expect(ConnectionURL.normalize("   http://host:7331   ") == "http://host:7331")
        #expect(ConnectionURL.normalize("\thost\n") == "http://host")
    }

    // MARK: Effective port

    @Test("effective port: explicit, http default 80, https default 443")
    func effectivePorts() {
        #expect(ConnectionURL.parse("http://h:9000")?.effectivePort == 9000)
        #expect(ConnectionURL.parse("http://h")?.effectivePort == 80)
        #expect(ConnectionURL.parse("https://h")?.effectivePort == 443)
        // Normalized string keeps explicit ports but never adds defaults.
        #expect(ConnectionURL.normalize("http://h") == "http://h")
        #expect(ConnectionURL.normalize("https://h") == "https://h")
    }

    // MARK: Duplicate detection

    private func record(_ url: String) -> ConnectionRecord {
        ConnectionRecord(name: "n", urlString: ConnectionURL.normalize(url)!, token: "")
    }

    @Test("same host+port across schemes is a duplicate")
    func dupAcrossSchemes() {
        let existing = [record("http://h:8080")]
        #expect(ConnectionURL.isDuplicate("https://h:8080", against: existing))
    }

    @Test("http vs https default ports do not collide")
    func differentEffectivePorts() {
        let existing = [record("http://h")] // port 80
        #expect(!ConnectionURL.isDuplicate("https://h", against: existing)) // port 443
    }

    @Test("same host, different explicit port is not a duplicate")
    func differentExplicitPort() {
        let existing = [record("http://h:1")]
        #expect(!ConnectionURL.isDuplicate("http://h:2", against: existing))
    }

    @Test("host comparison is case-insensitive")
    func caseInsensitiveHost() {
        let existing = [record("http://HOST")]
        #expect(ConnectionURL.isDuplicate("http://host", against: existing))
    }

    @Test("a record is excluded from its own duplicate check when editing")
    func selfExclusion() {
        let rec = record("http://h:7331")
        // Re-saving the same URL for the same record is not a duplicate.
        #expect(!ConnectionURL.isDuplicate("http://h:7331", against: [rec], excludingID: rec.id))
        // But another record with that host+port still collides.
        #expect(ConnectionURL.isDuplicate("http://h:7331", against: [rec], excludingID: UUID()))
    }
}

/// Store persistence, validation-on-mutation, and legacy migration.
@MainActor
@Suite("ConnectionsStore")
struct ConnectionsStoreTests {
    /// A fresh, isolated UserDefaults suite; the caller must clean it up.
    /// Credential store whose writes never stick — exercises the plaintext
    /// fallback path.
    private final class FailingCredentialStore: CredentialStore {
        func token(for id: UUID) -> String? { nil }
        func setToken(_ token: String, for id: UUID) -> Bool { false }
        func deleteToken(for id: UUID) {}
    }

    private func makeDefaults() -> (UserDefaults, String) {
        let suite = "ConnectionsStoreTests.\(UUID().uuidString)"
        return (UserDefaults(suiteName: suite)!, suite)
    }

    private func cleanup(_ defaults: UserDefaults, _ suite: String) {
        defaults.removePersistentDomain(forName: suite)
    }

    @Test("add appends in creation order and round-trips through UserDefaults")
    func persistenceRoundTrip() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }

        // Tokens round-trip through the credential store, not the JSON, so
        // the reload shares the same instance (as the real Keychain would).
        let credentials = InMemoryCredentialStore()
        let store = ConnectionsStore(defaults: defaults, credentials: credentials)
        try store.add(name: "First", urlString: "host-a:7331", token: "t1")
        try store.add(name: "Second", urlString: "https://host-b", token: "")
        #expect(store.connections.map(\.name) == ["First", "Second"])
        #expect(store.connections[0].urlString == "http://host-a:7331")
        #expect(store.connections[1].urlString == "https://host-b")

        // A new store reading the same defaults sees the same records/order.
        let reloaded = ConnectionsStore(defaults: defaults, credentials: credentials)
        #expect(reloaded.connections == store.connections)
        #expect(reloaded.connections[0].token == "t1")
    }

    @Test("empty name is rejected on add and update")
    func rejectsEmptyName() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())

        #expect(throws: ConnectionValidationError.emptyName) {
            try store.add(name: "   ", urlString: "http://h", token: "")
        }
        let rec = try store.add(name: "Ok", urlString: "http://h", token: "")
        #expect(throws: ConnectionValidationError.emptyName) {
            try store.update(id: rec.id, name: "", urlString: "http://h", token: "")
        }
    }

    @Test("invalid URL is rejected on add")
    func rejectsInvalidURL() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(throws: ConnectionValidationError.invalidURL) {
            try store.add(name: "Bad", urlString: "ftp://h", token: "")
        }
    }

    @Test("duplicate host+port is rejected on add and update")
    func rejectsDuplicate() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())

        try store.add(name: "A", urlString: "http://h:7331", token: "")
        // Same host+port, different scheme → duplicate.
        #expect(throws: ConnectionValidationError.duplicate) {
            try store.add(name: "B", urlString: "https://h:7331", token: "")
        }
        // A distinct host is fine, and can then be edited into a conflict.
        let b = try store.add(name: "B", urlString: "http://other:7331", token: "")
        #expect(throws: ConnectionValidationError.duplicate) {
            try store.update(id: b.id, name: "B", urlString: "http://h:7331", token: "")
        }
        // Editing a record to its own current URL is allowed (self-exclusion).
        try store.update(id: b.id, name: "B2", urlString: "http://other:7331", token: "x")
        #expect(store.connections.first { $0.id == b.id }?.name == "B2")
    }

    @Test("update on a missing id throws notFound")
    func updateMissing() {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(throws: ConnectionValidationError.notFound) {
            try store.update(id: UUID(), name: "X", urlString: "http://h", token: "")
        }
    }

    @Test("remove deletes and persists")
    func removePersists() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        let rec = try store.add(name: "A", urlString: "http://h", token: "")
        store.remove(id: rec.id)
        #expect(store.connections.isEmpty)
        #expect(ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore()).connections.isEmpty)
    }

    // MARK: Migration

    @Test("legacy valid URL migrates to one Connection with derived name and token, keys removed")
    func migratesValidLegacy() {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        defaults.set("http://workstation.tail1f9a09.ts.net:7331", forKey: "serverURLString")
        defaults.set("secret-token", forKey: "apiToken")

        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(store.connections.count == 1)
        let rec = store.connections[0]
        #expect(rec.name == "Workstation")
        #expect(rec.urlString == "http://workstation.tail1f9a09.ts.net:7331")
        #expect(rec.token == "secret-token")
        // Both legacy keys are gone.
        #expect(defaults.object(forKey: "serverURLString") == nil)
        #expect(defaults.object(forKey: "apiToken") == nil)
    }

    @Test("legacy IP host is kept whole as the name")
    func migratesIPHost() {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        defaults.set("http://127.0.0.1:7331", forKey: "serverURLString")

        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(store.connections.count == 1)
        #expect(store.connections[0].name == "127.0.0.1")
        #expect(store.connections[0].token == "")
    }

    @Test("no legacy keys yields an empty store without crashing")
    func noLegacy() {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(store.connections.isEmpty)
    }

    @Test("invalid legacy URL creates no Connection but still removes the keys")
    func invalidLegacy() {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        defaults.set("ftp://nope/path", forKey: "serverURLString")
        defaults.set("tok", forKey: "apiToken")

        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(store.connections.isEmpty)
        #expect(defaults.object(forKey: "serverURLString") == nil)
        #expect(defaults.object(forKey: "apiToken") == nil)
    }

    @Test("migration runs at most once and never duplicates")
    func migratesOnce() {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }
        defaults.set("http://host:7331", forKey: "serverURLString")

        let first = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(first.connections.count == 1)
        // Keys are gone, so a second init doesn't add another record.
        let second = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(second.connections.count == 1)
        #expect(second.connections == first.connections)
    }

    @Test("tokens never persist in UserDefaults once the credential store holds them")
    func tokensLeaveUserDefaults() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }

        let credentials = InMemoryCredentialStore()
        let store = ConnectionsStore(defaults: defaults, credentials: credentials)
        let rec = try store.add(name: "A", urlString: "http://h", token: "secret")

        #expect(credentials.token(for: rec.id) == "secret")
        let raw = try #require(defaults.data(forKey: "connections"))
        let persisted = try JSONDecoder().decode([ConnectionRecord].self, from: raw)
        #expect(persisted[0].token == "")
        // The in-memory record still exposes the token to the app.
        #expect(store.connections[0].token == "secret")
    }

    @Test("plaintext tokens in existing data migrate to the credential store on load")
    func migratesPlaintextTokens() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }

        // Pre-Keychain persisted shape: token inline in the JSON array.
        let legacy = [ConnectionRecord(name: "Old", urlString: "http://h:7331", token: "plain")]
        defaults.set(try JSONEncoder().encode(legacy), forKey: "connections")

        let credentials = InMemoryCredentialStore()
        let store = ConnectionsStore(defaults: defaults, credentials: credentials)
        #expect(store.connections[0].token == "plain")
        #expect(credentials.token(for: legacy[0].id) == "plain")
        let raw = try #require(defaults.data(forKey: "connections"))
        let persisted = try JSONDecoder().decode([ConnectionRecord].self, from: raw)
        #expect(persisted[0].token == "")
    }

    @Test("a failed credential write keeps the plaintext fallback so the token survives")
    func failedWriteKeepsPlaintext() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }

        let store = ConnectionsStore(defaults: defaults, credentials: FailingCredentialStore())
        let rec = try store.add(name: "A", urlString: "http://h", token: "secret")
        #expect(store.connections[0].token == "secret")
        let raw = try #require(defaults.data(forKey: "connections"))
        let persisted = try JSONDecoder().decode([ConnectionRecord].self, from: raw)
        #expect(persisted[0].token == "secret")

        // A later load with a working credential store completes the move.
        let credentials = InMemoryCredentialStore()
        let healed = ConnectionsStore(defaults: defaults, credentials: credentials)
        #expect(healed.connections[0].token == "secret")
        #expect(credentials.token(for: rec.id) == "secret")
    }

    @Test("remove deletes the stored token")
    func removeDeletesToken() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }

        let credentials = InMemoryCredentialStore()
        let store = ConnectionsStore(defaults: defaults, credentials: credentials)
        let rec = try store.add(name: "A", urlString: "http://h", token: "secret")
        store.remove(id: rec.id)
        #expect(credentials.token(for: rec.id) == nil)
    }

    @Test("migration does not run when the connections key already has data")
    func skipsMigrationWithExistingData() throws {
        let (defaults, suite) = makeDefaults()
        defer { cleanup(defaults, suite) }

        // Seed real connection data, then plant legacy keys alongside it.
        let seed = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        try seed.add(name: "Existing", urlString: "http://existing:1", token: "")
        defaults.set("http://legacy:7331", forKey: "serverURLString")
        defaults.set("tok", forKey: "apiToken")

        let store = ConnectionsStore(defaults: defaults, credentials: InMemoryCredentialStore())
        #expect(store.connections.map(\.name) == ["Existing"])
        // Legacy keys are still cleaned up so the attempt won't repeat.
        #expect(defaults.object(forKey: "serverURLString") == nil)
        #expect(defaults.object(forKey: "apiToken") == nil)
    }
}
