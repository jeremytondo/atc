import Foundation
import Observation
import CockpitAPI

/// Picker-workflow state for browsing the workstation's filesystem via
/// `/api/fs`. Not a generic tree engine - exactly the folder picker's
/// drill-down state. Knows nothing about views.
@Observable
final class RemoteFileBrowser {
    private let client: any CockpitClient

    /// `nil` while no directory is currently on screen.
    private(set) var listing: DirectoryListing?
    private(set) var isLoading = false
    var lastError: String?
    /// Highlighted Entry (List selection) - never the Chosen Folder.
    var highlightedPath: String?
    /// Path-field draft; synced to `currentPath` on every navigation.
    var typedPath: String = ""
    var showHidden = false {
        didSet {
            guard showHidden != oldValue, listing != nil else { return }
            reloadTask = Task { await self.reloadCurrentDirectory() }
        }
    }
    /// Awaitable handle for the hidden-toggle reload; tests await it for
    /// determinism.
    private(set) var reloadTask: Task<Void, Never>?

    init(client: any CockpitClient) {
        self.client = client
    }

    // MARK: - Derived

    /// The Chosen Folder candidate — the directory currently on screen.
    var currentPath: String? { listing?.path }

    /// Whether Up navigates to a parent directory.
    var canGoUp: Bool {
        guard let currentPath else { return false }
        return currentPath != "/"
    }

    /// Lexical path segments for the current directory.
    var breadcrumbs: [(label: String, path: String)] {
        guard let currentPath, !currentPath.isEmpty else { return [] }
        var crumbs: [(label: String, path: String)] = []
        var accumulated = ""
        for component in (currentPath as NSString).pathComponents {
            if component == "/" {
                accumulated = "/"
                crumbs.append((label: "/", path: "/"))
            } else {
                accumulated = accumulated == "/" ? "/\(component)" : "\(accumulated)/\(component)"
                crumbs.append((label: component, path: accumulated))
            }
        }
        return crumbs
    }

    // MARK: - Commands

    func open(path: String) async {
        guard let fetched = await list(path) else { return }
        apply(fetched)
    }

    func descend(into entry: RemoteEntry) async {
        guard entry.kind == .directory else { return }
        guard let fetched = await list(entry.path) else { return }
        apply(fetched)
    }

    func goUp() async {
        guard let currentPath, canGoUp else { return }
        let parent = (currentPath as NSString).deletingLastPathComponent
        guard let fetched = await list(parent) else { return }
        apply(fetched)
    }

    /// Return / Go on the path field. The server is the sole validator:
    /// the trimmed string goes straight to `fs/list` and a failure renders
    /// the typed error while navigation state stays put.
    func commitTypedPath() async {
        let trimmed = typedPath.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let fetched = await list(trimmed) else { return }
        apply(fetched)
    }

    /// Breadcrumb segment tap.
    func jump(to path: String) async {
        guard let fetched = await list(path) else { return }
        apply(fetched)
    }

    // MARK: - Internals

    private func reloadCurrentDirectory() async {
        guard let currentPath else { return }
        let highlighted = highlightedPath
        guard let fetched = await list(currentPath) else { return }
        apply(fetched)
        // Keep the highlight only if the entry survived the re-list.
        if let highlighted, fetched.entries.contains(where: { $0.path == highlighted }) {
            highlightedPath = highlighted
        }
    }

    private func list(_ path: String) async -> DirectoryListing? {
        isLoading = true
        defer { isLoading = false }
        do {
            let fetched = try await client.listDirectory(path: path, showHidden: showHidden)
            lastError = nil
            return fetched
        } catch {
            lastError = error.localizedDescription
            return nil
        }
    }

    private func apply(_ fetched: DirectoryListing) {
        listing = fetched
        typedPath = fetched.path
        highlightedPath = nil
    }
}
