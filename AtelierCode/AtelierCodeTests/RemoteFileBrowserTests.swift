import Foundation
import Testing
import CockpitAPI
@testable import AtelierCode

/// Drives the picker-workflow model against `MockCockpitClient`'s canned
/// tree (two roots; `secrets` throws permission_denied; `huge` is
/// truncated; `shared` is a symlinked directory).
@Suite("RemoteFileBrowser")
struct RemoteFileBrowserTests {
    let browser = RemoteFileBrowser(client: MockCockpitClient())

    private var projectsRoot: RemoteWorkspaceRoot {
        RemoteWorkspaceRoot(label: "Projects", path: "/home/dev/Projects")
    }

    private func entry(named name: String) throws -> RemoteEntry {
        try #require(browser.listing?.entries.first { $0.name == name })
    }

    @Test("loadRoots populates roots")
    func loadRoots() async {
        #expect(!browser.hasLoadedRoots)
        await browser.loadRoots()
        #expect(browser.hasLoadedRoots)
        #expect(browser.roots.map(\.label) == ["Projects", "Home"])
        #expect(browser.listing == nil)
    }

    @Test("open(root:) lists it and sets activeRoot")
    func openRoot() async {
        await browser.loadRoots()
        await browser.open(root: projectsRoot)
        #expect(browser.activeRoot == projectsRoot)
        #expect(browser.currentPath == "/home/dev/Projects")
        #expect(browser.typedPath == "/home/dev/Projects")
        #expect(browser.lastError == nil)
    }

    @Test("descend into directory updates state and clears highlight")
    func descendDirectory() async throws {
        await browser.open(root: projectsRoot)
        browser.highlightedPath = "/home/dev/Projects/notes.md"
        await browser.descend(into: try entry(named: "atelier"))
        #expect(browser.currentPath == "/home/dev/Projects/atelier")
        #expect(browser.typedPath == "/home/dev/Projects/atelier")
        #expect(browser.highlightedPath == nil)
    }

    @Test("descend into file or unknown is a no-op")
    func descendInert() async throws {
        await browser.open(root: projectsRoot)
        await browser.descend(into: try entry(named: "atelier"))

        let before = browser.listing
        await browser.descend(into: try entry(named: "README.md"))
        #expect(browser.listing == before)
        await browser.descend(into: try entry(named: "dangling"))
        #expect(browser.listing == before)
    }

    @Test("goUp stops at activeRoot, then returns to roots list")
    func goUpBoundary() async throws {
        await browser.open(root: projectsRoot)
        await browser.descend(into: try entry(named: "atelier"))
        #expect(browser.canGoUp)

        await browser.goUp()
        #expect(browser.currentPath == "/home/dev/Projects")
        #expect(!browser.canGoUp)

        await browser.goUp()
        #expect(browser.listing == nil)
        #expect(browser.activeRoot == nil)
        #expect(browser.typedPath.isEmpty)
    }

    @Test("breadcrumbs follow the lexical path through a symlinked dir")
    func breadcrumbs() async throws {
        await browser.open(root: projectsRoot)
        #expect(browser.breadcrumbs.map(\.label) == ["Projects"])

        await browser.descend(into: try entry(named: "atelier"))
        await browser.descend(into: try entry(named: "shared"))
        #expect(browser.breadcrumbs.map(\.label) == ["Projects", "atelier", "shared"])
        #expect(browser.breadcrumbs.map(\.path) == [
            "/home/dev/Projects",
            "/home/dev/Projects/atelier",
            "/home/dev/Projects/atelier/shared",
        ])
    }

    @Test("breadcrumb jump re-lists that segment")
    func breadcrumbJump() async throws {
        await browser.open(root: projectsRoot)
        await browser.descend(into: try entry(named: "atelier"))
        await browser.descend(into: try entry(named: "src"))

        await browser.jump(to: "/home/dev/Projects/atelier")
        #expect(browser.currentPath == "/home/dev/Projects/atelier")
        #expect(browser.activeRoot == projectsRoot)
    }

    @Test("commitTypedPath lists and recomputes activeRoot by longest prefix")
    func commitValidPath() async {
        // No loadRoots first — the prefill flow starts cold.
        browser.typedPath = "/home/dev/Projects/atelier/src"
        await browser.commitTypedPath()
        #expect(browser.currentPath == "/home/dev/Projects/atelier/src")
        // Projects wins over Home even though both contain the path.
        #expect(browser.activeRoot == projectsRoot)
        #expect(browser.hasLoadedRoots)
    }

    @Test("commitTypedPath failure keeps navigation state")
    func commitInvalidPath() async {
        await browser.open(root: projectsRoot)
        browser.typedPath = "/home/dev/Projects/nope"
        await browser.commitTypedPath()
        #expect(browser.lastError == "not found: /home/dev/Projects/nope")
        #expect(browser.currentPath == "/home/dev/Projects")
        #expect(browser.activeRoot == projectsRoot)
    }

    @Test("showHidden toggle re-lists the current directory")
    func hiddenToggle() async throws {
        await browser.open(root: projectsRoot)
        await browser.descend(into: try entry(named: "atelier"))
        #expect(browser.listing?.entries.contains { $0.name == ".gitignore" } == false)

        browser.showHidden = true
        await browser.reloadTask?.value
        #expect(browser.listing?.entries.contains { $0.name == ".gitignore" } == true)
        #expect(browser.currentPath == "/home/dev/Projects/atelier")
    }

    @Test("truncated listing exposes the flag")
    func truncated() async throws {
        await browser.open(root: projectsRoot)
        await browser.descend(into: try entry(named: "huge"))
        #expect(browser.listing?.truncated == true)
    }

    @Test("permission_denied surfaces the server message, listing survives")
    func permissionDenied() async throws {
        await browser.open(root: projectsRoot)
        await browser.descend(into: try entry(named: "secrets"))
        #expect(browser.lastError == "permission denied: /home/dev/Projects/secrets")
        #expect(browser.currentPath == "/home/dev/Projects")
    }

    @Test("chosen folder is the viewed directory, never the highlight")
    func chosenFolder() async throws {
        await browser.open(root: projectsRoot)
        browser.highlightedPath = try entry(named: "atelier").path

        // The sheet's Use This Folder wiring: onChoose(currentPath).
        var chosen: String?
        let onChoose: (String) -> Void = { chosen = $0 }
        if let path = browser.currentPath { onChoose(path) }
        #expect(chosen == "/home/dev/Projects")
    }
}
