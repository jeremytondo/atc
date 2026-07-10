import Foundation
import Security

/// Storage for Connection bearer tokens, keyed by the Connection's stable
/// UUID. Tokens live here instead of inside the JSON-encoded connection list
/// so they never sit in plain app preferences.
protocol CredentialStore {
    func token(for id: UUID) -> String?
    /// Stores (or, for an empty token, removes) the token. Returns whether
    /// the store now durably holds that state — callers keep their previous
    /// storage when this fails so a token is never lost.
    @discardableResult
    func setToken(_ token: String, for id: UUID) -> Bool
    func deleteToken(for id: UUID)
}

/// Keychain-backed store: one generic-password item per Connection in the
/// user's login keychain. The login keychain (rather than the data-protection
/// keychain) keeps unsigned developer builds working; both encrypt at rest.
final class KeychainCredentialStore: CredentialStore {
    private let service: String

    init(service: String = "ElevenIdeas.atc.connection-token") {
        self.service = service
    }

    func token(for id: UUID) -> String? {
        var query = baseQuery(id)
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        var result: CFTypeRef?
        guard SecItemCopyMatching(query as CFDictionary, &result) == errSecSuccess,
              let data = result as? Data else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func setToken(_ token: String, for id: UUID) -> Bool {
        if token.isEmpty {
            let status = SecItemDelete(baseQuery(id) as CFDictionary)
            return status == errSecSuccess || status == errSecItemNotFound
        }
        let data = Data(token.utf8)
        let update = [kSecValueData as String: data]
        let status = SecItemUpdate(baseQuery(id) as CFDictionary, update as CFDictionary)
        if status == errSecItemNotFound {
            var add = baseQuery(id)
            add[kSecValueData as String] = data
            return SecItemAdd(add as CFDictionary, nil) == errSecSuccess
        }
        return status == errSecSuccess
    }

    func deleteToken(for id: UUID) {
        _ = SecItemDelete(baseQuery(id) as CFDictionary)
    }

    private func baseQuery(_ id: UUID) -> [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: id.uuidString,
        ]
    }
}

/// Dictionary-backed store for tests and previews, so neither ever touches
/// the real keychain.
final class InMemoryCredentialStore: CredentialStore {
    private var tokens: [UUID: String] = [:]

    func token(for id: UUID) -> String? {
        tokens[id]
    }

    func setToken(_ token: String, for id: UUID) -> Bool {
        if token.isEmpty {
            tokens[id] = nil
        } else {
            tokens[id] = token
        }
        return true
    }

    func deleteToken(for id: UUID) {
        tokens[id] = nil
    }
}
