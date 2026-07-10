# Platform & Security Policy

Current, deliberate decisions for the macOS app and web admin UI. This is the
one place these policies live; build settings and code comments point here.
Decided 2026-07-10 during the foundation stabilization pass.

## Minimum macOS version: 26.0

The app, `packages/AtelierCodeKit` (`.macOS(.v26)`), and the macOS CI runner
all target macOS 26.0. Atelier Code is a personal/small-audience tool built on
current APIs; carrying an older floor nobody tests against would be false
compatibility. Raise the floor deliberately and in all three places at once.

## App Sandbox: off

The app disables App Sandbox because it:

- reads the user's Ghostty config (`~/.config/ghostty/config`) for terminal
  theming, and
- connects to arbitrary user-configured hosts (loopback, tailnet, LAN).

Re-enabling would require the `network.client` entitlement plus a
home-relative read exception for the Ghostty config, and would break nothing
else currently known. Revisit if the app is ever distributed beyond direct
developer-ID downloads (e.g. Mac App Store), where sandboxing is mandatory.
Hardened runtime stays on either way — releases are signed and notarized
(`scripts/release-dev.sh` validates this).

## Cleartext HTTP and ATS

Connections to `http://` origins are a supported first-class mode: the
canonical deployment is an Atelier Code server reached over a Tailscale
tailnet (WireGuard-encrypted underneath) or an SSH tunnel to loopback. The
connection editor therefore accepts cleartext origins without a warning —
nagging the primary use case teaches users to ignore warnings. ATS allows
arbitrary loads for the same reason; narrowing it is only worthwhile if the
app ever defaults to HTTPS-first public deployments.

The server-side counterpart of this policy (unauthenticated remote binds warn
but run) is documented in `server/README.md` under "Remote trust model".

## Native token storage: Keychain

Connection bearer tokens live in the login keychain, one generic-password
item per Connection UUID (`CredentialStore` in the macOS app). They are never
written to UserDefaults; a failed keychain write falls back to the old
plaintext persistence rather than losing the token, and migrates on the next
launch.

## Web admin token storage: localStorage

The embedded web UI keeps its bearer token in `localStorage`. It is a
trusted-local/tailnet admin tool served from the same origin as the API, not
a public product surface; session-only storage or cookie auth would add
friction without a matching threat. Revisit if the admin UI is ever exposed
beyond a trusted network.
