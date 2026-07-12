import SwiftUI
import ATCAPI

/// Drill-down folder picker over the server's `/api/fs`. Presented as a
/// nested sheet from `CreateProjectSheet`; commits the *viewed* directory
/// (never the highlighted row) via `onChoose`.
struct RemoteFolderPickerSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var browser: RemoteFileBrowser
    private let initialPath: String
    private let onChoose: (String) -> Void

    init(client: any ATCClient, initialPath: String = "", onChoose: @escaping (String) -> Void) {
        _browser = State(initialValue: RemoteFileBrowser(client: client))
        self.initialPath = initialPath.trimmingCharacters(in: .whitespaces)
        self.onChoose = onChoose
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            // Panes are child structs and their rows are inline in the
            // ForEach: rows built by view-returning helpers crash Xcode
            // preview dynamic replacement (ViewListTree identity assert).
            if browser.listing == nil {
                InitialDirectoryPane(browser: browser)
            } else {
                DirectoryListPane(browser: browser)
            }
            Divider()
            footer
        }
        .frame(width: 520, height: 480)
        .task {
            browser.typedPath = initialPath
            await browser.commitTypedPath()
        }
    }

    // MARK: - Header (path field, Up, breadcrumbs, error)

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                TextField("Path", text: $browser.typedPath, prompt: Text("/path/on/the/server"))
                    .textFieldStyle(.roundedBorder)
                    .autocorrectionDisabled()
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .onSubmit { Task { await browser.commitTypedPath() } }
                Button("Go") { Task { await browser.commitTypedPath() } }
            }
            HStack(spacing: 8) {
                Button {
                    Task { await browser.goUp() }
                } label: {
                    Image(systemName: "arrow.up")
                }
                .disabled(!browser.canGoUp)
                .help("Up")
                breadcrumbBar
            }
            if let message = browser.lastError {
                Label(message, systemImage: "exclamationmark.triangle")
                    .foregroundStyle(.red)
                    .font(.callout)
                    .lineLimit(2)
            }
        }
        .padding(12)
    }

    private var breadcrumbBar: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 3) {
                let crumbs = browser.breadcrumbs
                ForEach(Array(crumbs.enumerated()), id: \.element.path) { index, crumb in
                    if index > 0 {
                        Image(systemName: "chevron.right")
                            .font(.caption2)
                            .foregroundStyle(.tertiary)
                    }
                    Button(crumb.label) {
                        Task { await browser.jump(to: crumb.path) }
                    }
                    .buttonStyle(.plain)
                    .fontWeight(index == crumbs.count - 1 ? .semibold : .regular)
                    .lineLimit(1)
                }
            }
        }
        .frame(height: 20)
    }

    // MARK: - Footer

    private var footer: some View {
        HStack {
            Toggle("Show hidden files", isOn: $browser.showHidden)
                .toggleStyle(.checkbox)
            Spacer()
            Button("Cancel", role: .cancel) { dismiss() }
                .keyboardShortcut(.cancelAction)
            Button("Use This Folder") {
                guard let path = browser.currentPath else { return }
                onChoose(path)
                dismiss()
            }
            .keyboardShortcut(.defaultAction)
            .disabled(browser.currentPath == nil)
        }
        .padding(12)
    }
}

// MARK: - Initial pane

private struct InitialDirectoryPane: View {
    @Bindable var browser: RemoteFileBrowser

    var body: some View {
        if browser.isLoading {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            ContentUnavailableView(
                "No folder loaded",
                systemImage: "folder.badge.questionmark"
            )
        }
    }
}

// MARK: - Directory list pane

private struct DirectoryListPane: View {
    @Bindable var browser: RemoteFileBrowser

    var body: some View {
        VStack(spacing: 0) {
            if browser.listing?.truncated == true {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle")
                    Text("Listing truncated at 10,000 entries")
                }
                .font(.callout)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 12)
                .padding(.vertical, 6)
                Divider()
            }
            List(selection: $browser.highlightedPath) {
                ForEach(browser.listing?.entries ?? []) { entry in
                    let enterable = entry.kind == .directory
                    HStack {
                        Image(systemName: Self.icon(for: entry.kind))
                        Text(entry.name)
                        Spacer()
                    }
                    .foregroundStyle(enterable ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary))
                    .contentShape(Rectangle())
                    .onTapGesture(count: 2) {
                        guard enterable else { return }
                        Task { await browser.descend(into: entry) }
                    }
                    .tag(entry.path)
                }
            }
            .listStyle(.inset)
            .onKeyPress(.return) { descendHighlighted() }
            .overlay {
                if browser.isLoading {
                    ProgressView()
                } else if browser.listing?.entries.isEmpty == true {
                    Text("Empty folder")
                        .foregroundStyle(.secondary)
                }
            }
        }
    }

    private func descendHighlighted() -> KeyPress.Result {
        guard let path = browser.highlightedPath,
              let entry = browser.listing?.entries.first(where: { $0.path == path }) else { return .ignored }
        if entry.kind == .directory {
            Task { await browser.descend(into: entry) }
        }
        // Files and unknown entries consume the key and stay inert.
        return .handled
    }

    private static func icon(for kind: RemoteEntryKind) -> String {
        switch kind {
        case .directory: "folder"
        case .file: "doc"
        case .unknown: "questionmark.square.dashed"
        }
    }
}

#Preview("Default directory") {
    RemoteFolderPickerSheet(client: MockATCClient()) { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Directory") {
    RemoteFolderPickerSheet(client: MockATCClient(), initialPath: "/home/dev/Projects/atelier") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Prefill error") {
    RemoteFolderPickerSheet(client: MockATCClient(), initialPath: "/home/dev/Projects/secrets") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Truncated") {
    RemoteFolderPickerSheet(client: MockATCClient(), initialPath: "/home/dev/Projects/huge") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Empty folder") {
    RemoteFolderPickerSheet(client: MockATCClient(), initialPath: "/home/dev/Projects/empty") { _ in }
        .preferredColorScheme(.dark)
}
