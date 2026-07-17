# Session Lifecycle Simplification Brief

Status: Draft

## Purpose

Simplify Sessions around the lifecycle ATC can reliably observe: a Session is
either Live because its underlying ZMX session exists, or Ended because its ATC
record remains after ZMX has gone away. Delete becomes the only
user-requested Session lifecycle action, removing archive and stop concepts
that do not correspond to durable ZMX behavior.

## Idea Definition

An ATC Session record and its underlying ZMX session have related but distinct
lifetimes. A Live Session has an available ZMX session. An Ended Session retains
ATC metadata after ZMX is no longer available. Ended Sessions remain visible
until the user explicitly deletes them.

Deleting a Live Session ends ZMX first and then removes the ATC record. Deleting
an Ended Session removes only the record. A ZMX launch failure does not create a
user-visible Session.

## Recommended Direction

- Replace the public `starting`, `running`, `failed`, and `terminated` model
  with the canonical user-visible states `live` and `ended`.
- Keep startup as an internal provisional operation. A provisional record may
  reserve identity and protect launch races, but it must not appear in Session
  lists. Promote it to Live only after ZMX starts; remove it and return the
  launch error if startup fails.
- Remove Session archive and unarchive behavior end-to-end, including storage,
  API routes, response fields, list filters, client methods, macOS controls,
  web controls, documentation, and tests. Project and Workspace archival remain
  unchanged.
- Treat any observed disappearance of an established ZMX session as Ended.
  ATC should not claim to know whether the process completed successfully, was
  killed externally, or crashed when ZMX cannot provide that distinction.
- Keep Connection availability separate from Session lifecycle. An unreachable
  server does not imply that a Session ended; ATC preserves the last known state
  and reconciles when the Connection returns.
- Treat this as a pre-production breaking change. Update the canonical schema
  without compatibility or backfill logic, then reset the test database and
  remove the ATC-owned ZMX sessions associated with that instance so no orphan
  processes remain.

## Key Features

- Show Live and Ended Sessions together in the existing Sessions and Terminals
  groups, retaining the current title-only navigator rows.
- Selecting an Ended Session shows a clear `Session Ended` state with Delete
  available; it does not offer terminal interaction, Stop, Archive, or
  Unarchive.
- Confirm deletion with state-aware copy. Deleting a Live Session explains that
  its process will end and its record will be removed; deleting an Ended Session
  explains that its record will be removed permanently.
- If a Live Session disappears between refresh and interaction, reject the
  stale operation with a structured `session_ended` conflict. The client then
  reconciles the Session to Ended and presents `Session Ended` rather than a
  generic failure alert.
- Preserve robust partial-failure behavior:
  - If ending ZMX fails, keep the Session Live and report the error.
  - If ZMX ends but record deletion fails, keep the Session as Ended, report the
    error, and allow Delete to be retried.
- Keep files on disk and agent-harness history outside Session deletion. ATC
  removes only the ZMX session and its own Session record.

## Non-Goals / Deferred Ideas

- Session archiving, unarchiving, archived filters, or a dedicated Session
  archive surface.
- A separate Stop action or a user-visible stopped state.
- Automatic retention policies, periodic cleanup, or bulk deletion of Ended
  Sessions.
- Persisting terminal output or promising recoverable scrollback after ZMX is
  gone.
- Inferring process exit codes or distinguishing normal exit, external kill,
  and crash without reliable ZMX information.
- Changing Project or Workspace archive semantics.
- Changing agent-harness history or deleting files from a Workspace.

## System Shape

- **Session service and store**: Own the provisional-start, Live, Ended, and
  Delete transitions; reconcile persisted state against ZMX without treating
  reachability failures as process death.
- **Session API**: Expose only user-visible Sessions, Live/Ended status, Delete,
  and a structured stale-interaction conflict. Remove Session archive contracts
  and redundant archive filtering.
- **ATC clients**: Model Live and Ended consistently, reconcile
  `session_ended`, and remove archive-specific methods and state.
- **macOS and web surfaces**: Keep Ended records discoverable, present the
  selected Session's ended state clearly, and use one state-aware Delete flow.
- **Development reset**: Recreate the test data store and remove its ATC-owned
  ZMX sessions when the new schema is deployed.

## Core Concepts

- **Live Session**: A persisted, user-visible Session whose underlying ZMX
  session is currently known to exist.
- **Ended Session**: A persisted, user-visible Session whose underlying ZMX
  session is known to no longer exist.
- **Launch Attempt**: Internal provisional startup state that becomes a Live
  Session only after ZMX starts successfully.
- **Delete Session**: The sole user-requested Session lifecycle action. It ends
  ZMX when necessary and removes the ATC record only after ending succeeds.

## Confirmed Decisions

- Ended Sessions remain visible until explicitly deleted.
- Failed launch attempts never appear as Sessions.
- Session archive functionality is removed throughout the stack rather than
  hidden only in the macOS app.
- Existing test data is discarded instead of migrated.
- Session rows remain in their existing navigator groups; no Ended section,
  filter, icon, or status dot is added.
- `Session Ended` is a normal UI state, not a generic error.
- Workspace archival continues to require no Live Sessions. Ended Sessions do
  not block archival and remain associated with the archived Workspace.
- Workspace deletion continues to end Live Sessions and remove all associated
  Session records.
- Connection-unavailable state remains separate from Live/Ended status.
- Bulk deletion, automatic cleanup, transcript persistence, and inferred exit
  reasons remain out of scope.
