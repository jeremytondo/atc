package api

// DirectoryList is the GET /v1/directories response body (ATC-316): the
// immediate subdirectories of one directory on the server's machine, for
// clients that browse for a Space directory rather than typing an absolute
// path blind. Read-only and non-recursive by contract: no files, no hidden
// entries, no mutation.
type DirectoryList struct {
	Path      string           `json:"path" doc:"Canonical absolute directory that was listed (symlinks resolved)."`
	Parent    *string          `json:"parent" doc:"Canonical parent directory; null at the filesystem root."`
	Entries   []DirectoryEntry `json:"entries" doc:"Immediate subdirectories, hidden names excluded, sorted case-insensitively by name."`
	Truncated bool             `json:"truncated" doc:"Whether the listing stopped at the server's entry cap."`
}

// DirectoryEntry is one subdirectory in a DirectoryList.
type DirectoryEntry struct {
	Name string `json:"name" doc:"Entry name within the listed directory."`
	Path string `json:"path" doc:"Absolute path of the entry: the listed directory joined with the name."`
}
