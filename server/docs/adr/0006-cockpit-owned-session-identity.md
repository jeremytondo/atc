# Sessions use Cockpit-owned identity

Supersedes [ADR 0002](0002-session-identity-in-zmx-name.md).

Cockpit sessions will expose a stable Cockpit-owned `Session.id` as their public
identity instead of using the multiplexer session name as the API identifier.
The terminal-session proof loop encoded identity and scope in the `zmx` name to
avoid persistence, but the backend agent-sessions pass introduces a SQLite state
store and needs durable metadata, failed start records, archive behavior, and a
multiplexer boundary that can remain replaceable. Multiplexer names are private
implementation details derived deterministically from the `Session.id` inside the
multiplexer boundary — recomputable from the id, never separately persisted as
identity — and must not encode project, item, agent, or other user-facing session
meaning.
