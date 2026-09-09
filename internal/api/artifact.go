package api

import "time"

// Artifacts (ATC-318): published documents. An Artifact owns mutable
// browsing metadata and a history of immutable Versions; each Version is
// one publication of a self-contained static build plus the authoring
// snapshot that produced it. The main reader link resolves to the current
// (highest-numbered) version; every version keeps a permanent link.
// Reading needs no credential; every mutation and the source snapshot
// stay behind this API.

// Artifact is the mutable face of a published document.
type Artifact struct {
	ID    string `json:"id" doc:"Server-minted identifier; the reader URL's identity."`
	Title string `json:"title" doc:"Current display title; listings and the main link's reader header show it."`
	// ProjectID is the zero-or-one Project association; empty when
	// unassigned.
	ProjectID      string    `json:"projectId,omitempty" doc:"Project the artifact is organized under, when assigned."`
	CurrentVersion int       `json:"currentVersion" doc:"Number of the version the main link resolves to."`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt" doc:"Last publication or metadata change."`
	// URL is the main reader link on the local document origin; TailnetURL
	// the same link through the tailnet when exposure is enabled.
	URL        string `json:"url" doc:"Main reader link; always resolves to the current version."`
	TailnetURL string `json:"tailnetUrl,omitempty" doc:"Main reader link on the tailnet, when the document origin is exposed there."`
}

// ArtifactVersion is one immutable publication.
type ArtifactVersion struct {
	ArtifactID string `json:"artifactId"`
	Number     int    `json:"number" doc:"1-based position in the artifact's history."`
	Title      string `json:"title" doc:"Title at publication; the fixed link's reader header shows it."`
	// Platform identifies the authoring platform that produced the build,
	// as the publisher reported it.
	Platform    string    `json:"platform,omitempty" doc:"Authoring platform identity the publisher reported."`
	PublishedAt time.Time `json:"publishedAt"`
	// RestoredFrom names the version whose snapshots this one reuses; zero
	// for an uploaded build.
	RestoredFrom int            `json:"restoredFrom,omitempty" doc:"Version this one restores, when it is a restoration rather than an upload."`
	Source       ArtifactSource `json:"source" doc:"Source context recorded at publication."`
	URL          string         `json:"url" doc:"Permanent reader link for this version."`
	TailnetURL   string         `json:"tailnetUrl,omitempty" doc:"Permanent reader link on the tailnet, when the document origin is exposed there."`
}

// ArtifactSource is the provenance a publication records: where the
// document was authored. Everything is optional and preserved verbatim —
// history outlives the thread, project, or revision it names.
type ArtifactSource struct {
	Thread   string   `json:"thread,omitempty" maxLength:"100" doc:"ATC Thread the document was authored in."`
	Revision string   `json:"revision,omitempty" maxLength:"200" doc:"Repository revision the document describes."`
	Links    []string `json:"links,omitempty" maxItems:"20" doc:"Related issue, research, or reference links."`
}

// ArtifactPublishParams accompanies a publication's build and source
// archives (the `params` part of the multipart request). Creating an
// artifact and adding a version share it: BaseVersion is ignored on
// creation and required on an existing artifact, where it must name the
// current version. RestoreFrom names a stored version to republish in
// place of uploaded archives.
type ArtifactPublishParams struct {
	Title string `json:"title" minLength:"1" maxLength:"200" doc:"Title recorded on the version and set as the artifact's current title."`
	// BaseVersion is the version the publisher's working copy was based
	// on. It must be the current version, or publication fails with a
	// conflict naming the current one.
	BaseVersion int `json:"baseVersion,omitempty" minimum:"0" doc:"Version the publication is based on; must be current. Ignored when creating an artifact."`
	// PublicationID is the publisher's retry identity: a repeat of a
	// completed publication returns the version it committed instead of
	// appending another.
	PublicationID string `json:"publicationId" minLength:"1" maxLength:"128" doc:"Publisher-chosen retry identity; repeating a completed publication returns its result."`
	// RestoreFrom republishes a stored version's build and source as the
	// new current version; the archives are omitted.
	RestoreFrom int            `json:"restoreFrom,omitempty" minimum:"0" doc:"Stored version to republish instead of uploading archives."`
	Platform    string         `json:"platform,omitempty" maxLength:"200" doc:"Authoring platform identity."`
	Source      ArtifactSource `json:"source,omitzero" doc:"Source context to record."`
}

// ArtifactUpdateParams is a JSON Merge Patch of the mutable metadata.
type ArtifactUpdateParams struct {
	Title Optional[string] `json:"title,omitzero" minLength:"1" maxLength:"200" nullable:"false" doc:"New current title; no URL changes."`
	// ProjectID assigns a Project; null clears the association.
	ProjectID Optional[string] `json:"projectId,omitzero" doc:"Project to organize the artifact under; null unassigns it."`
}

// ArtifactPublication is a publication's result: the artifact as it now
// stands and the version it committed.
type ArtifactPublication struct {
	Artifact Artifact        `json:"artifact"`
	Version  ArtifactVersion `json:"version"`
}

// ArtifactList is the collection response.
type ArtifactList struct {
	Artifacts []Artifact `json:"artifacts"`
}

// ArtifactVersionList is an artifact's history, oldest first.
type ArtifactVersionList struct {
	Versions []ArtifactVersion `json:"versions"`
}
