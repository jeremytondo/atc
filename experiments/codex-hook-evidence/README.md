# Codex hook evidence POC

This directory records the live native-TUI proof of concept for
[ATC-281](https://linear.app/elevenideas/issue/ATC-281/prove-codex-hook-based-thread-evidence-with-a-live-tui-test).
It is historical experiment material, not production ATC hook or session code.

See [`findings.md`](findings.md) for the reviewed conclusions and
[`samples.jsonl`](samples.jsonl) for exact captured hook payloads from the
identity and status paths.

The test used `codex-cli 0.151.0`, a private `CODEX_HOME`, a disposable
workspace, and a test-owned Unix socket. It did not connect to the developer's
real Herdr socket or shared Codex app server.

## Fixtures

- `atc-281.config.toml` is the launch profile that declares the eight observed
  hook events. Its command path is specific to this checkout.
- `private-config.toml` is the minimal base configuration used in the private
  Codex home. The final `hooks.state` entry mirrors the already-trusted Herdr
  hook definition copied into that home.
- `capture_hook.py` writes one JSON record per invocation only when both
  `ATC_HOOK_CAPTURE_FILE` and `ATC_HOOK_TEST_ID` are present.
- `fake_herdr.py` accepts the existing Herdr hook's JSON-RPC report on a
  test-owned Unix socket and records it as JSONL.

The profile was installed as `$CODEX_HOME/atc-281.config.toml` and selected
with `codex -p atc-281`. Codex added hook trust hashes to that profile after
interactive review; those generated hashes are intentionally not committed.

