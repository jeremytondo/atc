import Foundation
import Testing
import AtelierCodeAPI
@testable import AtelierCode

/// Drives the picker-workflow model against `MockAtelierCodeClient`'s canned
/// tree (`secrets` throws permission_denied; `huge` is truncated;
/// `shared` is a symlinked directory).
@Suite("RemoteFileBrowser")
struct RemoteFileBrowserTests {
    let browser = RemoteFileBrowser(client: MockAtelierCodeClient())

    private let projectsPath = "/home/dev/Projects"

    private func entry(named name: String) throws -> RemoteEntry {
        try #require(browser.listing?.entries.first { $0.name == name })
    }

    @Test("empty path opens the server default directory")
    func emptyPathOpensDefaultDirectory() async {
        await browser.open(path: "")
        #expect(browser.currentPath == "/home/dev")
        #expect(browser.typedPath == "/home/dev")
        #expect(browser.lastError == nil)
    }

    @Test("open(path:) lists it")
    func openPath() async {
        await browser.open(path: projectsPath)
        #expect(browser.currentPath == "/home/dev/Projects")
        #expect(browser.typedPath == "/home/dev/Projects")
        #expect(browser.lastError == nil)
    }

    @Test("descend into directory updates state and clears highlight")
    func descendDirectory() async throws {
        await browser.open(path: projectsPath)
        browser.highlightedPath = "/home/dev/Projects/notes.md"
        await browser.descend(into: try entry(named: "atelier"))
        #expect(browser.currentPath == "/home/dev/Projects/atelier")
        #expect(browser.typedPath == "/home/dev/Projects/atelier")
        #expect(browser.highlightedPath == nil)
    }

    @Test("descend into file or unknown is a no-op")
    func descendInert() async throws {
        await browser.open(path: projectsPath)
        await browser.descend(into: try entry(named: "atelier"))

        let before = browser.listing
        await browser.descend(into: try entry(named: "README.md"))
        #expect(browser.listing == before)
        await browser.descend(into: try entry(named: "dangling"))
        #expect(browser.listing == before)
    }

    @Test("goUp navigates lexical parents and stops at filesystem root")
    func goUpBoundary() async throws {
        await browser.open(path: projectsPath)
        await browser.descend(into: try entry(named: "atelier"))
        #expect(browser.canGoUp)

        await browser.goUp()
        #expect(browser.currentPath == "/home/dev/Projects")

        await browser.goUp()
        #expect(browser.currentPath == "/home/dev")
        await browser.goUp()
        #expect(browser.currentPath == "/home")
        await browser.goUp()
        #expect(browser.currentPath == "/")
        #expect(!browser.canGoUp)

        await browser.goUp()
        #expect(browser.currentPath == "/")
    }

    @Test("breadcrumbs follow the lexical path through a symlinked dir")
    func breadcrumbs() async throws {
        await browser.open(path: projectsPath)
        #expect(browser.breadcrumbs.map(\.label) == ["/", "home", "dev", "Projects"])

        await browser.descend(into: try entry(named: "atelier"))
        await browser.descend(into: try entry(named: "shared"))
        #expect(browser.breadcrumbs.map(\.label) == ["/", "home", "dev", "Projects", "atelier", "shared"])
        #expect(browser.breadcrumbs.map(\.path) == [
            "/",
            "/home",
            "/home/dev",
            "/home/dev/Projects",
            "/home/dev/Projects/atelier",
            "/home/dev/Projects/atelier/shared",
        ])
    }

    @Test("breadcrumb jump re-lists that segment")
    func breadcrumbJump() async throws {
        await browser.open(path: projectsPath)
        await browser.descend(into: try entry(named: "atelier"))
        await browser.descend(into: try entry(named: "src"))

        await browser.jump(to: "/home/dev/Projects/atelier")
        #expect(browser.currentPath == "/home/dev/Projects/atelier")
    }

    @Test("commitTypedPath lists the typed path")
    func commitValidPath() async {
        browser.typedPath = "/home/dev/Projects/atelier/src"
        await browser.commitTypedPath()
        #expect(browser.currentPath == "/home/dev/Projects/atelier/src")
    }

    @Test("commitTypedPath failure keeps navigation state")
    func commitInvalidPath() async {
        await browser.open(path: projectsPath)
        browser.typedPath = "/home/dev/Projects/nope"
        await browser.commitTypedPath()
        #expect(browser.lastError == "not found: /home/dev/Projects/nope")
        #expect(browser.currentPath == "/home/dev/Projects")
    }

    @Test("showHidden toggle re-lists the current directory")
    func hiddenToggle() async throws {
        await browser.open(path: projectsPath)
        await browser.descend(into: try entry(named: "atelier"))
        #expect(browser.listing?.entries.contains { $0.name == ".gitignore" } == false)

        browser.showHidden = true
        await browser.reloadTask?.value
        #expect(browser.listing?.entries.contains { $0.name == ".gitignore" } == true)
        #expect(browser.currentPath == "/home/dev/Projects/atelier")
    }

    @Test("truncated listing exposes the flag")
    func truncated() async throws {
        await browser.open(path: projectsPath)
        await browser.descend(into: try entry(named: "huge"))
        #expect(browser.listing?.truncated == true)
    }

    @Test("permission_denied surfaces the server message, listing survives")
    func permissionDenied() async throws {
        await browser.open(path: projectsPath)
        await browser.descend(into: try entry(named: "secrets"))
        #expect(browser.lastError == "permission denied: /home/dev/Projects/secrets")
        #expect(browser.currentPath == "/home/dev/Projects")
    }

    @Test("chosen folder is the viewed directory, never the highlight")
    func chosenFolder() async throws {
        await browser.open(path: projectsPath)
        browser.highlightedPath = try entry(named: "atelier").path

        // The sheet's Use This Folder wiring: onChoose(currentPath).
        var chosen: String?
        let onChoose: (String) -> Void = { chosen = $0 }
        if let path = browser.currentPath { onChoose(path) }
        #expect(chosen == "/home/dev/Projects")
    }
}
