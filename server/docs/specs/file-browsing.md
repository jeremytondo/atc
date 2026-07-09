# Tech Design — Remote File Browsing: Atelier Code `/api/fs`

## Scope

Atelier Code exposes a read-only filesystem browsing API for the host running the
service:

- `GET /api/fs/list` — list the immediate children of one directory.
- Omitted or empty `path` defaults to the current user's home directory.
- Any absolute path is browseable if the Atelier Code process can read it.

No file reading, mutation, recursive walking, or search index is included.

## API Contract

`GET /api/fs/list?path=<abs>&showHidden=<bool>`

Query parameters:

| Param        | Required | Meaning                                               |
| ------------ | -------- | ----------------------------------------------------- |
| `path`       | no       | Absolute path to list. Empty or omitted means `$HOME`. |
| `showHidden` | no       | Include dot-prefixed entries. Default `false`.         |

Success response:

```json
{
  "path": "/home/jeremy/Projects",
  "truncated": false,
  "entries": [
    {
      "name": "src",
      "path": "/home/jeremy/Projects/src",
      "kind": "directory",
      "isSymlink": false,
      "modifiedAt": "2026-07-05T14:03:22.123456789Z"
    },
    {
      "name": "README.md",
      "path": "/home/jeremy/Projects/README.md",
      "kind": "file",
      "isSymlink": false,
      "size": 2048,
      "modifiedAt": "2026-06-28T18:12:45.5Z"
    }
  ]
}
```

Field semantics:

- `path` is the cleaned lexical path that was listed.
- `entries[].path` is `path` plus the entry name; path strings are entry
  identity.
- `kind` is `directory`, `file`, or `unknown`.
- Symlinks are classified by target when possible.
- Broken symlinks, sockets, FIFOs, devices, and unstatable entries are
  `unknown`.
- `size` is present only for files.
- `modifiedAt` is present for files and directories.
- `truncated` is true when the response was capped.

Errors use the existing envelope:

```json
{ "error": "not_found", "message": "not found: /missing" }
```

| Code                | HTTP | When                                                    |
| ------------------- | ---- | ------------------------------------------------------- |
| `invalid_path`      | 400  | Non-empty path is relative or contains a NUL byte.      |
| `not_found`         | 404  | Path does not exist.                                   |
| `not_directory`     | 400  | Path exists but is not a directory.                    |
| `permission_denied` | 403  | The requested directory cannot be read.                |
| `internal_error`    | 500  | Home resolution failure, timeout, or unexpected I/O.   |

`GET /api/fs/roots` and `[[fs.roots]]` configuration are not part of the
contract.

## Service Semantics

- Empty path resolves to `os.UserHomeDir()`, cleaned with `filepath.Clean`.
- Non-empty paths must be absolute and NUL-free, then are cleaned with
  `filepath.Clean`.
- The service never resolves the requested path for identity. The OS naturally
  follows symlinks during `os.Stat` and `os.ReadDir`.
- Listing is one directory per request and never recursive.
- Hidden entries are names beginning with `.` and are filtered before sorting
  and truncation.
- Sort order is directories first, dot-prefixed names first within each group,
  then case-insensitive name compare with a byte-wise tie-break.
- Responses are capped at 10,000 entries after hidden filtering.
- Each list call is bounded by a 10 second timeout.

## Implementation Notes

- `internal/fs` owns normalization, listing, classification, sorting, timeouts,
  and filesystem error mapping.
- `internal/api/fs.go` is a thin HTTP adapter for `GET /fs/list`.
- `internal/server/server.go` constructs `fs.NewService(logger)` directly.
- Config and CLI plumbing do not include filesystem roots.
- The API remains behind the existing `/api` auth boundary.

## Test Plan

- `internal/fs`: default-to-home, absolute path browsing, `/` browsing,
  normalization, symlink targets, hidden filtering, sorting, truncation,
  permissions, missing paths, non-directories, cancellation, and timeout.
- `internal/api`: success shape, default-to-home behavior, `showHidden`, error
  code mapping, invalid `showHidden`, and nil-service guard.
- `internal/server`: TCP token auth still protects `/api/fs/list`.
- Full validation: `go test ./...` and `git diff --check`.
