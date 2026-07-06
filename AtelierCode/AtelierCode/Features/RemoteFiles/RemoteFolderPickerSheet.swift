import SwiftUI
import CockpitAPI

/// Drill-down folder picker over Cockpit's `/api/fs`. Presented as a
/// nested sheet from `CreateSessionSheet`; commits the *viewed* directory
/// (never the highlighted row) via `onChoose`.
struct RemoteFolderPickerSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var browser: RemoteFileBrowser
    private let initialPath: String
    private let onChoose: (String) -> Void

    init(client: any CockpitClient, initialPath: String = "", onChoose: @escaping (String) -> Void) {
        _browser = State(initialValue: RemoteFileBrowser(client: client))
        self.initialPath = initialPath.trimmingCharacters(in: .whitespaces)
        self.onChoose = onChoose
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            if browser.listing == nil {
                rootsList
            } else {
                directoryList
            }
            Divider()
            footer
        }
        .frame(width: 520, height: 480)
        .task {
            if initialPath.isEmpty {
                await browser.loadRoots()
            } else {
                // Prefill from the working-dir field; on failure the roots
                // list shows with the error visible above.
                browser.typedPath = initialPath
                await browser.commitTypedPath()
            }
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
                .disabled(browser.listing == nil)
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

    // MARK: - Roots list

    @ViewBuilder
    private var rootsList: some View {
        if browser.isLoading && !browser.hasLoadedRoots {
            ProgressView()
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else if browser.roots.isEmpty && browser.hasLoadedRoots {
            ContentUnavailableView(
                "No browsable roots",
                systemImage: "folder.badge.questionmark",
                description: Text("Workspace roots are configured on the Cockpit server in the [fs] config section.")
            )
        } else {
            List(selection: $browser.highlightedPath) {
                ForEach(browser.roots) { root in
                    rootRow(root)
                        .tag(root.path)
                }
            }
            .listStyle(.inset)
            .onKeyPress(.return) { openHighlightedRoot() }
        }
    }

    private func rootRow(_ root: RemoteWorkspaceRoot) -> some View {
        HStack {
            Image(systemName: "folder")
            Text(root.label)
            Spacer()
            Text(root.path)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .truncationMode(.middle)
        }
        .contentShape(Rectangle())
        .onTapGesture(count: 2) {
            Task { await browser.open(root: root) }
        }
    }

    private func openHighlightedRoot() -> KeyPress.Result {
        guard let path = browser.highlightedPath,
              let root = browser.roots.first(where: { $0.path == path }) else { return .ignored }
        Task { await browser.open(root: root) }
        return .handled
    }

    // MARK: - Directory list

    private var directoryList: some View {
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
                    entryRow(entry)
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

    private func entryRow(_ entry: RemoteEntry) -> some View {
        let enterable = entry.kind == .directory
        return HStack {
            Image(systemName: icon(for: entry.kind))
            Text(entry.name)
            Spacer()
        }
        .foregroundStyle(enterable ? AnyShapeStyle(.primary) : AnyShapeStyle(.secondary))
        .contentShape(Rectangle())
        .onTapGesture(count: 2) {
            guard enterable else { return }
            Task { await browser.descend(into: entry) }
        }
    }

    private func icon(for kind: RemoteEntryKind) -> String {
        switch kind {
        case .directory: "folder"
        case .file: "doc"
        case .unknown: "questionmark.square.dashed"
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

#Preview("Roots") {
    RemoteFolderPickerSheet(client: MockCockpitClient()) { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Directory") {
    RemoteFolderPickerSheet(client: MockCockpitClient(), initialPath: "/home/dev/Projects/atelier") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Prefill error") {
    RemoteFolderPickerSheet(client: MockCockpitClient(), initialPath: "/home/dev/Projects/secrets") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Truncated") {
    RemoteFolderPickerSheet(client: MockCockpitClient(), initialPath: "/home/dev/Projects/huge") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("Empty folder") {
    RemoteFolderPickerSheet(client: MockCockpitClient(), initialPath: "/home/dev/Projects/empty") { _ in }
        .preferredColorScheme(.dark)
}

#Preview("No roots") {
    RemoteFolderPickerSheet(client: MockCockpitClient(mockRoots: [])) { _ in }
        .preferredColorScheme(.dark)
}
