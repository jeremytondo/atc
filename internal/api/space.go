package api

import "time"

// Space is the /v1/spaces resource (ATC-296): a container for live
// terminal sessions, the runtime organization terminals belong to. A
// space carries a name and a directory — the default working directory
// of terminals created in it — and nothing else: no project reference
// (clients derive one from the directory with the same containment rule
// threads use), no layout, no ordering. The server owns one Default
// space, rooted at the server user's home directory, that cannot be
// changed or deleted.
type Space struct {
	ID        string    `json:"id" doc:"Server-minted identifier."`
	Name      string    `json:"name" doc:"Display name. Not required to be unique."`
	Directory string    `json:"directory" doc:"Canonical absolute directory (symlinks resolved) terminals created in the space start in unless they name their own. Editable; a change affects only later terminals. Spaces may share or overlap directories."`
	IsDefault bool      `json:"isDefault" doc:"Whether this is the server's Default space, which rejects update and deletion."`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// SpaceCreateParams is the POST /v1/spaces request body.
type SpaceCreateParams struct {
	Directory string `json:"directory,omitempty" doc:"Directory terminals in the space start in; defaults to the server user's home directory. Canonicalized by the server; must exist and be a directory."`
	Name      string `json:"name,omitempty" doc:"Display name; defaults to the basename of the canonical directory."`
}

// SpaceUpdateParams is the PATCH /v1/spaces/{id} request body, a JSON
// Merge Patch: omitted fields are unchanged; neither field accepts null.
// The Default space refuses any update.
type SpaceUpdateParams struct {
	Name      Optional[string] `json:"name,omitzero" minLength:"1" nullable:"false" doc:"New display name."`
	Directory Optional[string] `json:"directory,omitzero" minLength:"1" nullable:"false" doc:"New directory for later terminals; canonicalized by the server, must exist and be a directory. Existing terminals keep theirs."`
}

// SpaceList is the GET /v1/spaces response body, in creation order.
type SpaceList struct {
	Spaces []Space `json:"spaces"`
}
