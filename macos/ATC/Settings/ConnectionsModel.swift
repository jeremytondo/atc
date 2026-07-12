import Foundation
import Observation

/// An app-local Connection to an atc server. Persisted as part of a
/// JSON-encoded `[ConnectionRecord]` array under a single UserDefaults key —
/// except `token`, which lives in the credential store (Keychain) keyed by
/// `id` and is stripped from the JSON before writing.
/// `id` is stable local identity, never shown to the user; `name` is
/// app-chosen and not required to be unique; `urlString` is always a
/// normalized, origin-only URL with an explicit `http`/`https` scheme;
/// `token` is `""` when no bearer token is configured.
struct ConnectionRecord: Codable, Identifiable, Hashable, Sendable {
    let id: UUID
    var name: String
    var urlString: String
    var token: String

    init(id: UUID = UUID(), name: String, urlString: String, token: String) {
        self.id = id
        self.name = name
        self.urlString = urlString
        self.token = token
    }
}

/// Typed outcome of validating a Connection draft. Surfaced by
/// `ConnectionsStore.add`/`update` so the Settings UI (Phase 2) can render
/// inline errors.
enum ConnectionValidationError: Error, Equatable, Sendable {
    case emptyName
    case invalidURL
    case duplicate
    /// The record being updated no longer exists in the store.
    case notFound
}

/// Pure, nonisolated URL validation and normalization for Connections.
/// Kept free of any store/UI state so it is trivially unit-testable.
enum ConnectionURL {
    /// A successfully parsed origin-only URL.
    struct Origin: Equatable, Sendable {
        /// Normalized origin string, e.g. `http://host:7331` (explicit scheme,
        /// no trailing slash, explicit port only when one was given).
        let urlString: String
        /// Host exactly as parsed (not lowercased).
        let host: String
        let scheme: String
        /// Explicit port if present, else 80 for http / 443 for https.
        let effectivePort: Int
    }

    /// Parses and normalizes a raw URL string. Returns `nil` when the input is
    /// not a valid origin-only http/https URL.
    ///
    /// Rules: trim whitespace; infer `http://` when no scheme is present (the
    /// returned string always carries an explicit scheme); scheme must be
    /// `http` or `https`; host is required; origin-only — a lone trailing `/`
    /// is stripped, any other path, query, fragment, or userinfo is invalid.
    static func parse(_ raw: String) -> Origin? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }

        // A scheme is present only when the string starts with `scheme://`.
        // Otherwise "host:7331" would be misread as scheme "host".
        let hasScheme = trimmed.range(
            of: "^[a-zA-Z][a-zA-Z0-9+.-]*://",
            options: .regularExpression
        ) != nil
        let candidate = hasScheme ? trimmed : "http://" + trimmed

        guard let comps = URLComponents(string: candidate) else { return nil }
        guard let scheme = comps.scheme?.lowercased(),
              scheme == "http" || scheme == "https" else { return nil }
        guard let host = comps.host, !host.isEmpty else { return nil }
        // Reject userinfo, query, and fragment.
        guard comps.user == nil, comps.password == nil,
              comps.query == nil, comps.fragment == nil else { return nil }
        // Origin-only: allow empty path or a single trailing slash.
        guard comps.path.isEmpty || comps.path == "/" else { return nil }

        var urlString = "\(scheme)://\(host)"
        if let port = comps.port {
            urlString += ":\(port)"
        }
        let effectivePort = comps.port ?? (scheme == "https" ? 443 : 80)
        return Origin(urlString: urlString, host: host, scheme: scheme, effectivePort: effectivePort)
    }

    /// Convenience returning just the normalized origin string.
    static func normalize(_ raw: String) -> String? {
        parse(raw)?.urlString
    }

    /// Local/remote context for the Dashboard's Connection sections: a
    /// loopback host reads "Local"; anything else reads as the remote host.
    static func contextLabel(for urlString: String) -> String {
        guard let origin = parse(urlString) else { return urlString }
        // URLComponents may keep an IPv6 host's brackets ("[::1]").
        let host = origin.host.lowercased()
            .trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        if host == "localhost" || host == "127.0.0.1" || host == "::1" {
            return "Local"
        }
        return origin.host
    }

    /// Whether `candidate` (a raw or normalized URL) duplicates any of
    /// `records` — same lowercased host and same effective port, scheme
    /// ignored. `excludingID`, when set, is skipped (a record never conflicts
    /// with itself while being edited).
    static func isDuplicate(
        _ candidate: String,
        against records: [ConnectionRecord],
        excludingID: UUID? = nil
    ) -> Bool {
        guard let cand = parse(candidate) else { return false }
        let candHost = cand.host.lowercased()
        for record in records {
            if let excludingID, record.id == excludingID { continue }
            guard let other = parse(record.urlString) else { continue }
            if other.host.lowercased() == candHost && other.effectivePort == cand.effectivePort {
                return true
            }
        }
        return false
    }
}

/// Owns the ordered `[ConnectionRecord]` list, validates every mutation, and
/// persists the whole array as JSON under the `connections` UserDefaults key.
/// Array order is creation order; new Connections append.
///
/// Main-actor-bound because the app is UI-driven and this feeds `@Observable`
/// views; the pure `ConnectionURL` helpers stay nonisolated for testing.
@MainActor
@Observable
final class ConnectionsStore {
    private(set) var connections: [ConnectionRecord] = []

    private let defaults: UserDefaults
    private let credentials: any CredentialStore

    private enum Keys {
        static let connections = "connections"
        static let legacyURL = "serverURLString"
        static let legacyToken = "apiToken"
    }

    init(defaults: UserDefaults = .standard, credentials: any CredentialStore = KeychainCredentialStore()) {
        self.defaults = defaults
        self.credentials = credentials
        let hadPlaintextTokens = load()
        migrateLegacyIfNeeded()
        // One-time migration per record: any token found in the persisted
        // JSON moves to the credential store on the next persist, which
        // strips it from UserDefaults only after a verified write.
        if hadPlaintextTokens { persist() }
    }

    // MARK: Mutations

    /// Validates and appends a new Connection, persisting on success.
    /// Throws `ConnectionValidationError` on empty name, invalid URL, or a
    /// host+port duplicate of an existing Connection.
    @discardableResult
    func add(name: String, urlString: String, token: String) throws -> ConnectionRecord {
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedName.isEmpty else { throw ConnectionValidationError.emptyName }
        guard let origin = ConnectionURL.normalize(urlString) else {
            throw ConnectionValidationError.invalidURL
        }
        guard !ConnectionURL.isDuplicate(origin, against: connections) else {
            throw ConnectionValidationError.duplicate
        }
        let record = ConnectionRecord(name: trimmedName, urlString: origin, token: token)
        connections.append(record)
        persist()
        return record
    }

    /// Validates and updates an existing Connection in place (preserving its
    /// position), persisting on success. Throws `.notFound` when no record has
    /// `id`, else the same validation errors as `add`.
    func update(id: UUID, name: String, urlString: String, token: String) throws {
        guard let index = connections.firstIndex(where: { $0.id == id }) else {
            throw ConnectionValidationError.notFound
        }
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmedName.isEmpty else { throw ConnectionValidationError.emptyName }
        guard let origin = ConnectionURL.normalize(urlString) else {
            throw ConnectionValidationError.invalidURL
        }
        guard !ConnectionURL.isDuplicate(origin, against: connections, excludingID: id) else {
            throw ConnectionValidationError.duplicate
        }
        connections[index].name = trimmedName
        connections[index].urlString = origin
        connections[index].token = token
        persist()
    }

    /// Removes the Connection with `id` (no-op if absent) and persists.
    func remove(id: UUID) {
        let before = connections.count
        connections.removeAll { $0.id == id }
        if connections.count != before {
            credentials.deleteToken(for: id)
            persist()
        }
    }

    // MARK: Persistence

    /// Loads the persisted list, hydrating tokens from the credential store.
    /// Returns whether any persisted record still carried a plaintext token
    /// (pre-Keychain data, or a previously failed credential write).
    private func load() -> Bool {
        guard let data = defaults.data(forKey: Keys.connections),
              let decoded = try? JSONDecoder().decode([ConnectionRecord].self, from: data) else {
            return false
        }
        connections = decoded.map { record in
            var record = record
            if record.token.isEmpty, let stored = credentials.token(for: record.id) {
                record.token = stored
            }
            return record
        }
        return decoded.contains { !$0.token.isEmpty }
    }

    private func persist() {
        let records = connections.map { record in
            var copy = record
            // Strip the token from the JSON only once the credential store
            // durably holds it; on failure the plaintext stays in
            // UserDefaults (the pre-Keychain behavior) so the token is never
            // lost, and the next persist retries the migration.
            if credentials.setToken(record.token, for: record.id) {
                copy.token = ""
            }
            return copy
        }
        guard let data = try? JSONEncoder().encode(records) else { return }
        defaults.set(data, forKey: Keys.connections)
    }

    // MARK: Legacy migration

    /// Migrates the old single-server settings (`serverURLString` / `apiToken`)
    /// into one Connection, then deletes both legacy keys. Runs at most once,
    /// keyed off the presence of the legacy URL key: once deleted it cannot
    /// re-run. Skips creating a Connection when the list already has data or
    /// the legacy URL is invalid, but still removes the legacy keys so the
    /// attempt never repeats.
    private func migrateLegacyIfNeeded() {
        guard defaults.object(forKey: Keys.legacyURL) != nil else { return }
        defer {
            defaults.removeObject(forKey: Keys.legacyURL)
            defaults.removeObject(forKey: Keys.legacyToken)
        }
        // Don't duplicate if the new store already holds data.
        guard connections.isEmpty else { return }
        guard let legacy = defaults.string(forKey: Keys.legacyURL),
              let origin = ConnectionURL.parse(legacy) else { return }
        let token = defaults.string(forKey: Keys.legacyToken) ?? ""
        let record = ConnectionRecord(
            name: Self.derivedName(fromHost: origin.host),
            urlString: origin.urlString,
            token: token
        )
        connections.append(record)
        persist()
    }

    /// Derives a display name from a host: the first host label, capitalized
    /// (`workstation.tail1f9a09.ts.net` → `Workstation`). IP addresses are kept
    /// whole (`127.0.0.1` → `127.0.0.1`).
    static func derivedName(fromHost host: String) -> String {
        let isIPv4 = host.allSatisfy { $0.isNumber || $0 == "." }
        let isIPv6 = host.contains(":")
        if isIPv4 || isIPv6 { return host }
        let firstLabel = host.split(separator: ".").first.map(String.init) ?? host
        return firstLabel.capitalized
    }
}
