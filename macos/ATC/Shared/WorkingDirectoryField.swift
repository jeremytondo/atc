// The one editor for a server-host absolute directory, shared by the
// creation sheets and the New Thread composer's directory chip. The server
// is the sole authority on whether a path is
// usable, so the field re-checks through `checkDirectory` whenever the typed
// value settles and publishes the outcome; the creation sheets gate
// submission on `DirectoryCheckState.isAvailable` rather than inspecting
// paths themselves, while the New Thread composer lets the server's create
// refuse an unusable path on the first send instead.
// Any edit to the path or connection drops the previous verdict to
// `.checking` before the debounce, so a gate can never submit on a verdict
// that belongs to an earlier value.
//
// Choose… opens the server-backed DirectoryPickerSheet (`GET /fs/list`), so
// the browsed filesystem is the server's on every Connection — but
// validation, never the picker, is what makes a path trustworthy.

import ATCAppServerAPI
import ATCDesign
import SwiftUI

enum DirectoryCheckState: Equatable {
    /// Nothing typed yet: no verdict, no message.
    case idle
    case checking
    case checked(Components.Schemas.DirectoryState, String?)
    /// The check itself could not be made (no Connection, unreachable server).
    case failed(String)

    var isAvailable: Bool {
        guard case .checked(.available, _) = self else { return false }
        return true
    }
}

struct WorkingDirectoryField: View {
    let label: String
    @Binding var path: String
    /// The Connection the path is checked against; nil disables checking.
    let client: (any APIProtocol)?
    /// Identity of that Connection: the verdict is a function of
    /// (path, server), so switching servers must re-run the check even
    /// when the typed path is unchanged.
    let connectionID: UUID?
    @Binding var state: DirectoryCheckState
    @State private var isBrowsing = false

    private struct CheckKey: Equatable {
        let path: String
        let connectionID: UUID?
    }

    var body: some View {
        VStack(alignment: .leading, spacing: Spacing.xs) {
            HStack(spacing: Spacing.sm) {
                textField
                Button("Choose…") { isBrowsing = true }
                    .disabled(client == nil)
            }
            if let message = statusMessage {
                Label(message.text, systemImage: message.systemImage)
                    .font(.caption)
                    .foregroundStyle(message.color)
                    .lineLimit(2)
            }
        }
        .task(id: CheckKey(path: path, connectionID: connectionID)) { await check() }
        // A picker browsing one server must not survive into another (or no)
        // Connection; without this, losing the client mid-browse would leave
        // an empty, undismissable sheet.
        .onChange(of: connectionID) { isBrowsing = false }
        .sheet(isPresented: $isBrowsing) {
            if let client {
                DirectoryPickerSheet(
                    client: client,
                    // Start where the user already is when the typed path is
                    // known-good; otherwise the server home.
                    initialPath: state.isAvailable
                        ? path.trimmingCharacters(in: .whitespaces) : nil,
                    onChoose: { path = $0 }
                )
            }
        }
    }

    private var textField: some View {
        TextField(label, text: $path, prompt: Text("/path/on/the/server"))
            .autocorrectionDisabled()
    }

    private func check() async {
        let trimmed = path.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else {
            state = .idle
            return
        }
        guard trimmed.hasPrefix("/") else {
            state = .failed("Enter an absolute path, starting with /.")
            return
        }
        guard let client else {
            state = .failed("Select a connection first.")
            return
        }
        // Any change to (path, server) invalidates the previous verdict
        // right away — gates must never submit on a verdict for a value
        // that is no longer the one in the field.
        state = .checking
        // Settle the keystroke burst before spending a server round trip;
        // `.task(id:)` cancels this on the next edit.
        do {
            try await Task.sleep(for: .milliseconds(300))
        } catch {
            return
        }
        do {
            let response = try await client.checkDirectory(query: .init(path: trimmed)).ok.body.json
            state = .checked(response.state, response.reason)
        } catch {
            state = .failed(error.localizedDescription)
        }
    }

    private var statusMessage: (text: String, systemImage: String, color: Color)? {
        switch state {
        case .idle:
            return nil
        case .checking:
            return ("Checking…", "clock", .secondary)
        case .failed(let reason):
            return (reason, "exclamationmark.triangle.fill", .orange)
        case .checked(let directoryState, let reason):
            let text = Self.description(of: directoryState)
            let detail = reason.map { "\(text) — \($0)" } ?? text
            return directoryState == .available
                ? (detail, "checkmark.circle.fill", .green)
                : (detail, "exclamationmark.triangle.fill", .orange)
        }
    }

    private static func description(of state: Components.Schemas.DirectoryState) -> String {
        switch state {
        case .available: "Directory found"
        case .missing: "No directory at that path"
        case .inaccessible: "The server cannot read that directory"
        case .notDirectory: "That path is not a directory"
        case .unknown: "The directory could not be checked"
        }
    }
}
