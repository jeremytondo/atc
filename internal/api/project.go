package api

import "time"

// Project is the /v1/projects resource (ATC-256, ATC-295): durable
// codebase context. A project is rooted at a canonical directory;
// threads whose initial directory lies under it are classified into it,
// the most specific project winning when projects nest. Projects own no
// terminals — runtime organization is the space's (ATC-296).
type Project struct {
	ID string `json:"id" doc:"Server-minted identifier."`
	// Name is editable and deliberately not unique; the canonical
	// directory is unique.
	Name      string    `json:"name" doc:"Display name. Not required to be unique."`
	Directory string    `json:"directory" doc:"Canonical absolute directory (symlinks resolved) the project is rooted at. Unique across projects; projects may nest. Editable — changing it backfills unassigned threads under the new directory and never rewrites existing associations."`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProjectCreateParams is the POST /v1/projects request body.
type ProjectCreateParams struct {
	Directory string `json:"directory" minLength:"1" doc:"Directory the project is rooted at. Canonicalized by the server; must exist and be a directory."`
	Name      string `json:"name,omitempty" doc:"Display name; defaults to the basename of the canonical directory."`
}

// ProjectUpdateParams is the PATCH /v1/projects/{id} request body, a JSON
// Merge Patch: omitted fields are unchanged; neither field accepts null.
type ProjectUpdateParams struct {
	Name      Optional[string] `json:"name,omitzero" minLength:"1" nullable:"false" doc:"New display name."`
	Directory Optional[string] `json:"directory,omitzero" minLength:"1" nullable:"false" doc:"New root directory; canonicalized by the server, must exist and be a directory, and must not belong to another project."`
}

// ProjectList is the GET /v1/projects response body.
type ProjectList struct {
	Projects []Project `json:"projects"`
}
