import Foundation
import Observation
import CockpitAPI

/// Picker-workflow state for browsing the workstation's filesystem via
/// `/api/fs`. Not a generic tree engine — exactly the folder picker's
/// drill-down state. Knows nothing about views.
@Observable
final class RemoteFileBrowser {
    private let client: any CockpitClient

    private(set) var roots: [RemoteWorkspaceRoot] = []
    private(set) var activeRoot: RemoteWorkspaceRoot?
    /// `nil` ⇒ showing the roots list.
    private(set) var listing: DirectoryListing?
    private(set) var isLoading = false
    private(set) var hasLoadedRoots = false
    var lastError: String?
    /// Highlighted Entry (List selection) — never the Chosen Folder.
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

    /// Whether Up navigates to a parent directory (false at the active
    /// root's top and on the roots list). `goUp()` at the root still works:
    /// it returns to the roots list.
    var canGoUp: Bool {
        guard let currentPath else { return false }
        return currentPath != activeRoot?.path
    }

    /// Active root's label followed by the lexical path segments below it.
    var breadcrumbs: [(label: String, path: String)] {
        guard let currentPath, let activeRoot else { return [] }
        var crumbs = [(label: activeRoot.label, path: activeRoot.path)]
        guard currentPath != activeRoot.path else { return crumbs }
        let relative = String(currentPath.dropFirst(activeRoot.path.count).drop(while: { $0 == "/" }))
        var accumulated = activeRoot.path
        for segment in relative.split(separator: "/") {
            accumulated += "/\(segment)"
            crumbs.append((label: String(segment), path: accumulated))
        }
        return crumbs
    }

    // MARK: - Commands

    func loadRoots() async {
        isLoading = true
        defer {
            isLoading = false
            hasLoadedRoots = true
        }
        do {
            roots = try await client.workspaceRoots()
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    func open(root: RemoteWorkspaceRoot) async {
        guard let fetched = await list(root.path) else { return }
        activeRoot = root
        apply(fetched)
    }

    func descend(into entry: RemoteEntry) async {
        guard entry.kind == .directory else { return }
        guard let fetched = await list(entry.path) else { return }
        apply(fetched)
    }

    func goUp() async {
        guard let currentPath else { return }
        if !canGoUp {
            // At the active root's top: back out to the roots list.
            listing = nil
            activeRoot = nil
            typedPath = ""
            highlightedPath = nil
            return
        }
        let parent = (currentPath as NSString).deletingLastPathComponent
        guard let fetched = await list(parent) else { return }
        apply(fetched)
    }

    /// Return / Go on the path field. The server is the sole validator:
    /// the trimmed string goes straight to `fs/list` and a failure renders
    /// the typed error while navigation state stays put.
    func commitTypedPath() async {
        if !hasLoadedRoots {
            await loadRoots()
        }
        let trimmed = typedPath.trimmingCharacters(in: .whitespacesAndNewlines)
        guard let fetched = await list(trimmed) else { return }
        activeRoot = longestPrefixRoot(of: fetched.path)
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

    /// The root whose path is the longest lexical prefix of `path`. The
    /// server already guaranteed containment, so a match exists whenever
    /// the roots are current.
    private func longestPrefixRoot(of path: String) -> RemoteWorkspaceRoot? {
        roots
            .filter { path == $0.path || path.hasPrefix($0.path == "/" ? "/" : $0.path + "/") }
            .max { $0.path.count < $1.path.count }
    }
}
