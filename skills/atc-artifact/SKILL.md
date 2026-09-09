---
name: atc-artifact
description: Author and publish an ATC artifact — a polished, interactive HTML document (explanation, comparison, report, demonstration) built on ATC's shared React platform and served from ATC's document origin with a persistent link. Use when a user wants a browser-readable document rather than a chat answer or a Markdown file, when revising a published artifact, or when returning links to earlier versions.
---

# ATC artifacts

An artifact is a published document: one stable link that always shows the newest version, and a permanent link for every version ever published. Each version stores a complete static build and the source it was built from. Artifacts are **historical snapshots**: a version explains things as they were when it was published, and nobody maintains it afterwards. Say so in the document when the topic is a moving target, and record the revision you looked at.

You author in a **working copy**: a ready-made Vite/React project on ATC's fixed platform, living under ATC's authoring directory — never inside a project repository. The platform (components, theme, dependencies, build) is fixed and shared; you write only what is under `src/document`.

## Responsibilities

- **You (the lead agent)** own the research, the structure, the prose, and the review of the finished page.
- **Code — the React in `src/document` and any fixes to it — prefers Codex Sol with high reasoning.** A Codex lead delegates to Sol/high; a Claude lead shells out. The working copy is not a git repository, so Codex needs `--skip-git-repo-check`, and `-C` takes the copy's `directory` (not its `document` subdirectory):

  ```sh
  codex exec --skip-git-repo-check --model gpt-5.6-sol --config 'model_reasoning_effort="high"' -C <directory> "<handoff>" </dev/null
  ```

  This runs the operator's own Codex CLI and account, and sends the working copy's contents to it, like any other Codex delegation on this machine. It is a cost preference, not a prerequisite: if that route is unavailable or impractical, write the code yourself with whatever model you are, without stopping to ask.

A coding handoff carries: the **content brief** (what the document says, in order, with the facts and numbers), the **working copy location** (`directory` to run in, and `document` — `src/document` — as the only tree to edit), the **platform constraints** (below), and the **checks** to run (`atc artifact check <copy>` must pass; `atc artifact build <copy>` when the output is worth inspecting).

## Workflow

1. **Create a working copy.**

   ```sh
   atc artifact new --title "How the scheduler works" --example architecture --json
   ```

   Examples: `architecture` (explanation with diagram and navigation), `comparison` (tables, tabs, filters, stat tiles), `demo` (interactive simulation). Omit `--example` for the starter; omit `--title` to take the example's. `--json` prints `{id, title, directory, document, …}`; without it the same fields come as a table. The title is the document's heading and the reader header's; the document never carries its own. The first run installs a private Node runtime and the platform's dependencies (a one-time download, reported on stderr); later copies reuse them.

2. **Write the document** under `document` (`src/document` in the copy). `index.tsx` exports a default component. Import shared components from `@/platform`. Add files freely under `src/document` (data modules, extra components, images). Nothing else in the copy is yours: the rest is the platform, resynced on every open and build, and **only the platform and `src/document` go into a build and its source archive** — files placed elsewhere are ignored, so keep assets under `src/document`.

3. **Check** while iterating: `atc artifact check <copy>` type-checks against the current platform and prints errors with file and line. A type error after an ATC upgrade means the platform changed; adapt the document. `atc artifact build <copy>` also produces the static site (under the copy's `.atc/dist`) when you want to inspect it; `publish` runs the check and build itself.

4. **Preview (optional, local only).** `atc artifact preview <copy>` runs the dev server in the foreground and prints the URL; edits reload. Run it in a background terminal (an ATC terminal works) and stop it before publishing — the copy is locked while it runs.

5. **Publish.**

   ```sh
   atc artifact publish <copy> --thread <thread-id> --link https://linear.app/... --json
   ```

   Provenance is optional and never invented: `--thread` is the ATC thread you are working in when you know it, `--link` (repeatable) the issues or references the document relates to, `--revision` the repository revision it describes — defaulting to `git rev-parse HEAD` of the directory you run the command from, so run it from the repository the document is about, or pass it explicitly. `--title` changes the title. The result carries the **latest** link and the **version** link, each with a tailnet form (`tailnetUrl`) when the server is exposed there. Give the user the tailnet link when present, otherwise the local one, and the version link when they will want to cite exactly this publication. `atc server status` shows whether the document origin is serving (`documents:` lines); publication succeeds even while it is down, so check before promising a link works.

The normal loop is edit, check, publish, return the link. There is no draft state and nothing to approve.

## Revising and retrieving

- **Find artifacts:** `atc artifact list [--project <proj-id>]` (id, title, current version, link) and `atc artifact get <id>`.
- **Revise a published artifact:** `atc artifact new --from <artifact-id>` takes the current version's document into a new working copy, based on that version. Publishing from it creates the next version. `--from <id>@<n>` starts from an older version instead (see conflicts below).
- **Read the history:** `atc artifact versions <id>` lists every version with its permanent link (local and tailnet). One version's source is a gzip tar of the document, the platform of its time, and the build configuration:

  ```sh
  atc artifact source <id> <n> -o - | tar -xzf - -C "$(mktemp -d)"   # then read src/document there
  ```
- **Restore an old version as the newest:** `atc artifact restore <id> <n>`. Its stored build is republished unchanged — no rebuild, no platform involvement.
- **Rename, organize, remove:** `atc artifact update <id> --title …`, `--project <proj-id>` / `--no-project`, `atc artifact delete <id>` (whole history; links stop resolving; working copies are untouched).
- **Working copies:** `atc artifact copies` lists them (`--json` too), `atc artifact open <copy>` reopens one (and brings it to the current platform), `atc artifact discard <copy>` removes one. `<copy>` is the id or any path inside the copy. They persist across publications until discarded.

### Conflicts

Every publication names the version the copy was based on. If someone published in between, `publish` (and `restore`) fails: the command exits non-zero with an HTTP 409 message naming the current version (the API's code is `artifact_base_stale`) — nothing is overwritten or merged. Recover deliberately:

1. `atc artifact versions <id>` to see what was published since your base.
2. Extract the current version's source (above) or open it as a copy (`atc artifact new --from <id>`), compare `src/document`, and fold in what you need.
3. `atc artifact publish <copy> --base <current>` (or `atc artifact restore <id> <n> --base <current>`).

A publication whose response was lost (network trouble) is safe to repeat: the copy keeps the exact package it uploaded as *pending*, the next `publish` resends it unchanged and reports that it did (`resent a pending publication`; `"resumed": true` with `--json`), and the server returns the same version rather than publishing twice. That invocation's flags and any edits made in the meantime go out with the publish after that.

## Platform constraints

- **Fixed dependencies:** `react`, `react-dom`, `radix-ui`, `lucide-react`, `prism-react-renderer`, `class-variance-authority`, `clsx`, `tailwind-merge`, Tailwind CSS v4. No new packages, no per-document build configuration.
- **Self-contained:** a published page may load only its own published assets. There is **no network** (`fetch`, `XMLHttpRequest`, WebSockets are blocked), no workers, no iframes or embedded external content, no form submission. Fonts, images, and data ship in the build: put them under `src/document` and import them.
- **No persistence:** reader state (filters, tabs, theme) resets on reload by design. Do not rely on `localStorage`.
- **Import every asset; never write a path string.** An imported file (`import diagram from "./diagram.svg"`) is bundled and pinned to the version's permanent path; a literal `src="./x.png"` resolves against whichever link the reader opened and breaks on the main link. Anchor links to other sections are unnecessary — `Section` registers itself with the navigation, which scrolls by script.
- Diagrams are SVG in JSX (see the `architecture` example); charts are SVG driven by React state (see `demo`).

## Components (`@/platform`)

| Import | Use |
| --- | --- |
| `Document` (`summary`, `navigation`) | Page shell: the title as heading, a summary, prose typography, section navigation on wide screens. |
| `Section` (`id`, `title`, `level` 2 or 3) | A titled section; registers itself with the navigation. |
| `Callout` (`kind`: note, tip, success, warning, danger; `title`) | Highlighted aside. |
| `CodeBlock` (`code`, `language`, `title`, `highlight`, `showLineNumbers`) | Syntax-highlighted block, themed with the reader. Inline code is plain `<code>`. |
| `Table`, `TableHeader`, `TableBody`, `TableFooter`, `TableRow`, `TableHead`, `TableCell`, `TableCaption` | Tables (`Table` scrolls horizontally when needed). |
| `Tabs`, `TabsList`, `TabsTrigger`, `TabsContent` | Tabbed panels. |
| `Collapsible`, `CollapsibleTrigger`, `CollapsibleContent` | Disclosure. |
| `Columns` (`count`), `Figure` (`caption`), `KeyValue` (`items`), `Stat` (`label`, `value`, `detail`) | Layout helpers: side-by-side columns, captioned figures, label/value lists, statistic tiles. |
| `Card`, `CardHeader`, `CardTitle`, `CardDescription`, `CardContent`, `CardFooter`, `Button`, `Badge` | Building blocks. |
| `cn()` | Class merging for custom components. |

Everything inside `Document` gets prose styling (`h2`, `p`, `ul`, `blockquote`, inline `code`, links); the platform's own blocks opt out with the `not-prose` class, and so can a custom block that manages its own layout. Tailwind utility classes and the theme colors (`bg-primary`, `text-muted-foreground`, `border-border`, `bg-code`, `text-success`, `text-warning`, `text-destructive`) are available; both appearances are covered by the theme, so do not hard-code colors.

The reader header (title, publication date, version, history, older-version notice, theme toggle) is the platform's; do not rebuild it in the document.

## Framing history correctly

A published version's header shows when it was published and which version it is; the main link's header shows the artifact's current title. When a document describes code, name the revision (`--revision`) and, in the prose, the date or version the description reflects. A later reader of the latest link must not mistake it for a claim about the code as it is today. If the explanation needs to stay true over time, publish new versions; the old ones remain reachable.
