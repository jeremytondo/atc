# SQLite stores Cockpit-owned state

Cockpit will use a local SQLite database for Cockpit-owned application state,
starting with the persistent session registry. Earlier specs deliberately
avoided persistence for the terminal-session proof loop, but the next backend
pass needs durable session ids, start failures, lifecycle timestamps, and
archive behavior that cannot be recovered from the multiplexer. SQLite is the
state store rather than a session-specific JSON file so future Cockpit state can
share one local database, migration path, and transaction boundary.

Schema changes should use an established Go migration library with ordered SQL
files embedded in the binary. The service applies pending migrations at startup
and fails clearly if the database cannot be opened or migrated. The database
lives under Cockpit's XDG state directory by default, with a config/env override
for tests and alternate profiles.
