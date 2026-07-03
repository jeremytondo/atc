import Foundation
import Observation

/// UserDefaults-backed app settings. Plain token storage is a POC
/// concession; Keychain is the stretch goal.
@Observable
final class AppSettings {
    static let defaultServerURLString = "http://workstation.tail1f9a09.ts.net:7331"

    private enum Keys {
        static let serverURL = "serverURLString"
        static let token = "apiToken"
    }

    var serverURLString: String {
        didSet { UserDefaults.standard.set(serverURLString, forKey: Keys.serverURL) }
    }

    var token: String {
        didSet { UserDefaults.standard.set(token, forKey: Keys.token) }
    }

    init() {
        let defaults = UserDefaults.standard
        serverURLString = defaults.string(forKey: Keys.serverURL) ?? Self.defaultServerURLString
        token = defaults.string(forKey: Keys.token) ?? ""
    }

    var serverURL: URL? {
        let trimmed = serverURLString.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty, let url = URL(string: trimmed), url.scheme != nil else {
            return nil
        }
        return url
    }
}
