// Server-backed folder browser (ATC-151): Choose… always lists the server's
// filesystem through `GET /fs/list` on the selected Connection — loopback or
// remote, one code path. Directories only, dotfolders excluded server-side;
// the text field stays the escape hatch for hidden paths. Read-only: no New
// Folder, no file selection.

import ATCAppServerAPI
import SwiftUI

@MainActor @Observable
final class DirectoryPickerModel {
    private let client: any APIProtocol

    private(set) var listing: Components.Schemas.FsListResponse?
    private(set) var errorMessage: String?
    private(set) var isLoading = false

    init(client: any APIProtocol) {
        self.client = client
    }

    /// The canonical path Choose commits: the directory currently listed.
    var currentPath: String? { listing?.path }

    var canGoUp: Bool { listing?.parent != nil }

    /// The picker's opening sequence: the given start when it lists, else
    /// the server home — a stale typed path never leaves a dead picker.
    func start(at initialPath: String?) async {
        await load(path: initialPath)
        if listing == nil && initialPath != nil {
            await load(path: nil)
        }
    }

    /// List `path` (nil = the server's home directory). One request at a
    /// time: taps landing while a load is in flight are dropped, so a slow
    /// response can never overwrite a newer one. A failed load keeps the
    /// last good listing so navigation survives a directory that vanished
    /// after it was listed.
    func load(path: String?) async {
        guard !isLoading else { return }
        isLoading = true
        defer { isLoading = false }
        do {
            switch try await client.listDirectory(query: .init(path: path)) {
            case .ok(let ok):
                listing = try ok.body.json
                errorMessage = nil
            case .unprocessableContent(let failure):
                // The server's tagged diagnostic (missing / inaccessible /
                // not a directory / timed out) already reads well.
                let body = try failure.body.json
                errorMessage = body.value1?.message ?? body.value2?.message
                    ?? "The server couldn't list that folder."
            case .undocumented(statusCode: let status, _):
                errorMessage = "Unexpected server response (\(status))."
            }
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func descend(into entry: Components.Schemas.FsListEntry) async {
        await load(path: entry.path)
    }

    func goUp() async {
        guard let parent = listing?.parent else { return }
        await load(path: parent)
    }
}

struct DirectoryPickerSheet: View {
    /// Listed first; nil (or a failed first load) starts at the server home.
    let initialPath: String?
    var onChoose: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var model: DirectoryPickerModel

    init(client: any APIProtocol, initialPath: String?, onChoose: @escaping (String) -> Void) {
        self.initialPath = initialPath
        self.onChoose = onChoose
        _model = State(initialValue: DirectoryPickerModel(client: client))
    }

    var body: some View {
        SheetScaffold(
            title: "Choose Folder",
            systemImage: "folder",
            primaryLabel: "Choose",
            // Not while loading: the model keeps the last good listing during
            // a load, so mid-flight Choose would commit the folder the user
            // just navigated away from.
            canSubmit: model.currentPath != nil && !model.isLoading,
            wrapsContentInForm: false,
            onCancel: { dismiss() },
            onSubmit: {
                guard let path = model.currentPath else { return }
                onChoose(path)
                dismiss()
            }
        ) {
            pathBar
            Divider()
            entryList
            if let message = model.errorMessage {
                Divider()
                Label(message, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption)
                    .foregroundStyle(.orange)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(Spacing.md)
            }
        }
        .frame(width: 480, height: 420)
        .task { await model.start(at: initialPath) }
    }

    private var pathBar: some View {
        HStack(spacing: Spacing.sm) {
            Button {
                Task { await model.goUp() }
            } label: {
                Image(systemName: "arrow.up")
            }
            .help("Enclosing folder")
            .disabled(!model.canGoUp || model.isLoading)
            Text(model.currentPath ?? " ")
                .font(.callout.monospaced())
                .lineLimit(1)
                .truncationMode(.middle)
                .frame(maxWidth: .infinity, alignment: .leading)
            if model.isLoading {
                ProgressView().controlSize(.small)
            }
        }
        .padding(.horizontal, Spacing.md)
        .padding(.vertical, Spacing.sm)
    }

    private var entryList: some View {
        List(model.listing?.entries ?? [], id: \.path) { entry in
            Button {
                Task { await model.descend(into: entry) }
            } label: {
                Label(entry.name, systemImage: "folder")
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(model.isLoading)
        }
        .listStyle(.inset)
        .overlay {
            if let listing = model.listing, listing.entries.isEmpty {
                Text("No subfolders")
                    .foregroundStyle(.secondary)
            }
        }
    }
}
