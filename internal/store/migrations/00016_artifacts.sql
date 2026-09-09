-- Artifacts (ATC-318): published documents. An artifact owns mutable
-- browsing metadata (title, optional project) and a history of immutable
-- versions; each version is one publication whose build and source
-- snapshots live on disk under the artifact's id and the version's
-- number (internal/artifacts). The current version is the highest number:
-- versions are appended, never removed individually, so no separate
-- reference can drift. Timestamps are fixed-width RFC 3339 UTC text
-- (store.TimeFormat).
-- +goose Up
CREATE TABLE artifacts (
    id TEXT PRIMARY KEY,
    title TEXT NOT NULL,
    -- Zero or one project. Deleting a project clears the reference; the
    -- artifact and its versions survive unassigned.
    project_id TEXT REFERENCES projects (id) ON DELETE SET NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

CREATE INDEX artifacts_project ON artifacts (project_id);

CREATE TABLE artifact_versions (
    artifact_id TEXT NOT NULL REFERENCES artifacts (id) ON DELETE CASCADE,
    -- 1-based, dense, per artifact; the reader URL's version identifier.
    number INTEGER NOT NULL,
    -- Title at publication: a fixed version link keeps showing it after
    -- the artifact is renamed.
    title TEXT NOT NULL,
    -- Identity of the authoring platform that produced the build; "" when
    -- the publisher reported none.
    platform TEXT NOT NULL,
    published_at TEXT NOT NULL,
    -- The version whose snapshots this one is a copy of, for restorations;
    -- NULL for an uploaded build.
    restored_from INTEGER,
    -- The publisher's retry identity: a repeat of a completed publication
    -- finds this row instead of appending another version.
    publication_id TEXT NOT NULL UNIQUE,
    -- Source context, preserved as reported: the authoring thread's id
    -- (no reference — history outlives the thread), the repository
    -- revision, and related links as a JSON array of strings.
    thread_id TEXT,
    revision TEXT,
    links TEXT NOT NULL,
    PRIMARY KEY (artifact_id, number)
) STRICT;
