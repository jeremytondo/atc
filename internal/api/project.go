package api

import "time"

// Project is the /v1/projects resource (ATC-256): ATC's unit of
// organization. Everything belongs to exactly one project; the directory
// answers where new things in the project start, asked once at create
// time.
type Project struct {
	ID string `json:"id" doc:"Server-minted identifier."`
	// Name is editable and deliberately not unique; the canonical
	// directory is the identity.
	Name      string    `json:"name" doc:"Display name; the only mutable field. Not required to be unique."`
	Directory string    `json:"directory" doc:"Canonical absolute directory (symlinks resolved) the project is rooted at. Unique across projects. Immutable."`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// ProjectCreateParams is the POST /v1/projects request body.
type ProjectCreateParams struct {
	Directory string `json:"directory" minLength:"1" doc:"Directory the project is rooted at. Canonicalized by the server; must exist and be a directory."`
	Name      string `json:"name,omitempty" doc:"Display name; defaults to the basename of the canonical directory."`
}

// ProjectUpdateParams is the PATCH /v1/projects/{id} request body. Name is
// the only mutable field; unknown or immutable fields are rejected.
type ProjectUpdateParams struct {
	Name string `json:"name" minLength:"1" doc:"New display name."`
}

// ProjectList is the GET /v1/projects response body.
type ProjectList struct {
	Projects []Project `json:"projects"`
}
