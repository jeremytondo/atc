import SwiftUI
import ATCAPI

/// What the editor pane is currently editing: an existing record by ID, or a
/// brand-new draft that isn't in the store until Save.
enum ConnectionEditorTarget: Hashable {
    case existing(UUID)
    case new
}

/// The Connections settings section: a master list of Connections on the left
/// (name, URL, status dot, and a +/− bottom bar) and a draft editor on the
/// right. Nothing touches `ConnectionsStore` until the editor's Save.
struct ConnectionsSettingsView: View {
    @Environment(AppModel.self) private var appModel

    @State private var target: ConnectionEditorTarget?
    @State private var confirmDelete = false

    private var store: ConnectionsStore { appModel.connections }

    private var selectedExistingID: UUID? {
        if case .existing(let id) = target { return id }
        return nil
    }

    private var selectedRecord: ConnectionRecord? {
        selectedExistingID.flatMap { id in store.connections.first { $0.id == id } }
    }

    var body: some View {
        HStack(spacing: 0) {
            master
                .frame(width: 240)
            Divider()
            detail
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .confirmationDialog(
            "Remove “\(selectedRecord?.name ?? "Connection")”?",
            isPresented: $confirmDelete
        ) {
            Button("Remove Connection", role: .destructive) {
                if let id = selectedExistingID {
                    appModel.removeConnection(id: id)
                    target = nil
                }
            }
        } message: {
            Text("This removes the connection from atc only. Its Projects and Terminal Sessions remain on the atc server.")
        }
    }

    // MARK: Master list

    private var master: some View {
        VStack(spacing: 0) {
            List(selection: $target) {
                ForEach(store.connections) { record in
                    HStack(spacing: Spacing.sm) {
                        StatusDot(color: appModel.reachability(of: record.id).color)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(record.name)
                                .font(.headline)
                                .lineLimit(1)
                            Text(record.urlString)
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                    }
                    .padding(.vertical, 2)
                    .tag(ConnectionEditorTarget.existing(record.id))
                }
            }
            Divider()
            ListEditorBar(
                addHelp: "Add a connection",
                removeHelp: "Remove the selected connection",
                canRemove: selectedExistingID != nil,
                onAdd: { target = .new },
                onRemove: { confirmDelete = true }
            )
        }
    }

    // MARK: Detail

    @ViewBuilder
    private var detail: some View {
        if let target {
            ConnectionEditorView(target: target) { savedID in
                self.target = .existing(savedID)
            }
            // Reseed drafts cleanly whenever the target changes.
            .id(target)
        } else if store.connections.isEmpty {
            ContentUnavailableView {
                Label("No Connections Configured", systemImage: "network.slash")
            } description: {
                Text("Add a connection to an atc server to see its projects and sessions in atc.")
            } actions: {
                Button("Add Connection") { target = .new }
            }
        } else {
            ContentUnavailableView(
                "No Connection Selected",
                systemImage: "sidebar.right",
                description: Text("Choose a connection to edit, or add a new one.")
            )
        }
    }
}

/// Draft editor for one Connection. Holds local `@State` copies seeded from the
/// selected record (or empty for a new draft); nothing reaches the store until
/// Save. Recreated per target via `.id(target)`, so seeding happens in `.task`.
private struct ConnectionEditorView: View {
    @Environment(AppModel.self) private var appModel
    let target: ConnectionEditorTarget
    /// Called after a successful Save with the record's ID so the parent can
    /// keep it selected (a new draft becomes an existing selection).
    var onSaved: (UUID) -> Void

    @State private var name = ""
    @State private var urlString = ""
    @State private var token = ""
    @State private var saveError: String?
    /// URL/token change with live terminal attaches: confirm before the
    /// runtime rebuild disconnects them. Cancel aborts the save entirely.
    @State private var confirmRebuild = false

    @State private var testState: TestState = .idle
    /// Bumped on every draft edit and every new test; stale results are dropped.
    @State private var testGeneration = 0

    private enum TestState: Equatable {
        case idle
        case testing
        case success(String)
        case failure(String)
    }

    private var store: ConnectionsStore { appModel.connections }

    private var currentRecord: ConnectionRecord? {
        if case .existing(let id) = target {
            return store.connections.first { $0.id == id }
        }
        return nil
    }

    private var hasChanges: Bool {
        if let record = currentRecord {
            return name != record.name || urlString != record.urlString || token != record.token
        }
        // New draft: enable Save once anything has been typed.
        return !name.isEmpty || !urlString.isEmpty || !token.isEmpty
    }

    private var canTest: Bool {
        ConnectionURL.parse(urlString) != nil
    }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section {
                    TextField("Name", text: $name)
                    TextField("URL", text: $urlString)
                        .autocorrectionDisabled()
                    SecureField("Token (optional)", text: $token)
                } footer: {
                    if let saveError {
                        Text(saveError)
                            .font(.caption)
                            .foregroundStyle(.red)
                    }
                }
                Section {
                    HStack(spacing: Spacing.sm) {
                        Button("Test Connection") { testConnection() }
                            .disabled(!canTest)
                        if testState == .testing {
                            ProgressView().controlSize(.small)
                        }
                        switch testState {
                        case .idle, .testing:
                            EmptyView()
                        case .success(let message):
                            Label(message, systemImage: "checkmark.circle.fill")
                                .font(.caption)
                                .foregroundStyle(.green)
                                .lineLimit(2)
                        case .failure(let message):
                            Label(message, systemImage: "exclamationmark.triangle.fill")
                                .font(.caption)
                                .foregroundStyle(.red)
                                .lineLimit(2)
                        }
                        Spacer()
                    }
                }
            }
            .formStyle(.grouped)

            Divider()
            HStack {
                Spacer()
                Button("Cancel") { seed() }
                    .disabled(!hasChanges)
                Button("Save") { save() }
                    .keyboardShortcut(.defaultAction)
                    .disabled(!hasChanges)
            }
            .padding(Spacing.md)
        }
        .task(id: target) { seed() }
        .onChange(of: name) { invalidateTest() }
        .onChange(of: urlString) { invalidateTest() }
        .onChange(of: token) { invalidateTest() }
        .confirmationDialog(
            "Save and disconnect terminals?",
            isPresented: $confirmRebuild
        ) {
            Button("Save and Disconnect", role: .destructive) { performSave() }
        } message: {
            Text("Changing the URL or token reconnects this connection, and its open terminal sessions will be disconnected. Sessions keep running on the server.")
        }
    }

    // MARK: Actions

    /// Reseed drafts from the current record (or clear for a new draft) and
    /// reset transient editor state.
    private func seed() {
        if let record = currentRecord {
            name = record.name
            urlString = record.urlString
            token = record.token
        } else {
            name = ""
            urlString = ""
            token = ""
        }
        saveError = nil
        testState = .idle
        testGeneration += 1
    }

    private func save() {
        saveError = nil
        if let record = currentRecord,
           appModel.wouldRebuildConnection(id: record.id, urlString: urlString, token: token),
           appModel.hasLiveTerminals(connectionID: record.id) {
            confirmRebuild = true
            return
        }
        performSave()
    }

    private func performSave() {
        do {
            if let record = currentRecord {
                try appModel.updateConnection(id: record.id, name: name, urlString: urlString, token: token)
                onSaved(record.id)
            } else {
                let record = try appModel.addConnection(name: name, urlString: urlString, token: token)
                onSaved(record.id)
            }
        } catch let error as ConnectionValidationError {
            saveError = Self.message(for: error)
        } catch {
            saveError = error.localizedDescription
        }
    }

    /// Any draft edit invalidates an in-flight test and clears stale results.
    private func invalidateTest() {
        testGeneration += 1
        if testState != .idle && testState != .testing {
            testState = .idle
        }
    }

    private func testConnection() {
        guard let origin = ConnectionURL.parse(urlString),
              let url = URL(string: origin.urlString) else { return }
        testGeneration += 1
        let generation = testGeneration
        let draftToken = token
        testState = .testing
        Task {
            let client = HTTPATCClient(server: ATCServer(baseURL: url, token: draftToken))
            do {
                _ = try await client.health()
                let version = try await client.version()
                guard generation == testGeneration else { return }
                testState = .success("Connected — \(version.name) \(version.version)")
            } catch {
                guard generation == testGeneration else { return }
                testState = .failure(error.localizedDescription)
            }
        }
    }

    private static func message(for error: ConnectionValidationError) -> String {
        switch error {
        case .emptyName:
            return "Enter a name for this connection."
        case .invalidURL:
            return "Enter a valid URL, including scheme — e.g. http://127.0.0.1:7331"
        case .duplicate:
            return "Another connection already uses this host and port."
        case .notFound:
            return "This connection no longer exists."
        }
    }
}

#Preview("Connections — populated") {
    let store = ConnectionsStore(defaults: UserDefaults(suiteName: "preview.connections.populated")!, credentials: InMemoryCredentialStore())
    _ = try? store.add(name: "Workstation", urlString: "http://workstation.tail1f9a09.ts.net:7331", token: "")
    _ = try? store.add(name: "Local Dev", urlString: "http://127.0.0.1:7331", token: "")
    return ConnectionsSettingsView()
        .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        .frame(width: 700, height: 450)
        .preferredColorScheme(.dark)
}

#Preview("Connections — empty") {
    let store = ConnectionsStore(defaults: UserDefaults(suiteName: "preview.connections.empty")!, credentials: InMemoryCredentialStore())
    return ConnectionsSettingsView()
        .environment(AppModel(connections: store, clientFactory: { _ in MockATCClient() }))
        .frame(width: 700, height: 450)
        .preferredColorScheme(.dark)
}
