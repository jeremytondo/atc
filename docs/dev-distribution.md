# Dev Distribution

This workflow produces a notarized Developer ID DMG for Apple Silicon Macs. It
does not add an app updater framework or any runtime release-checking behavior.

## App Identity

- App name: `AtelierCode Dev.app`
- Bundle ID: `ElevenIdeas.AtelierCode.dev`
- Artifact: DMG
- Release tag format: `dev-YYYYMMDD-HHMMSS`
- Architecture: Apple Silicon `arm64`
- Apple Team ID: `337D6CNU4E`
- Notary profile: `ateliercode-notary`

## One-Time Setup

Install or create a valid Developer ID Application certificate for team
`337D6CNU4E`, including its private key, on the release Mac.

Check the local signing identities:

```sh
security find-identity -v -p codesigning
```

The output must include a valid `Developer ID Application` identity whose team
ID is `337D6CNU4E`.

Store notarization credentials in the Keychain profile used by the release
script:

```sh
xcrun notarytool store-credentials ateliercode-notary \
  --apple-id "you@example.com" \
  --team-id 337D6CNU4E \
  --password "app-specific-password"
```

You can also use App Store Connect API key credentials with
`xcrun notarytool store-credentials` if that is preferred.

For GitHub upload support, authenticate the GitHub CLI:

```sh
gh auth login
gh auth status -h github.com
```

## Local Release

From the repository root, run:

```sh
scripts/release-dev.sh
```

The script creates a timestamped working directory under
`.build/release-dev/`, archives the app with the shared `AtelierCode` scheme,
exports it with Developer ID signing, creates a DMG, notarizes and staples the
DMG, then verifies the result with `codesign`, `stapler`, and `spctl`.

The final DMG path is printed at the end, for example:

```text
.build/release-dev/20260704-143015/AtelierCode-Dev-20260704-143015.dmg
```

## GitHub Prerelease Upload

To create a GitHub prerelease and upload the notarized DMG:

```sh
scripts/release-dev.sh --upload
```

This creates:

- Tag: `dev-YYYYMMDD-HHMMSS`
- Title: `AtelierCode Dev YYYYMMDD-HHMMSS`
- Prerelease: yes
- Asset: `AtelierCode-Dev-YYYYMMDD-HHMMSS.dmg`

The script only requires `gh` authentication when `--upload` is used.

## Overrides

The script supports these environment overrides:

```sh
AC_TEAM_ID=337D6CNU4E
AC_NOTARY_PROFILE=ateliercode-notary
AC_ARTIFACT_ROOT=.build/release-dev
```

Example:

```sh
AC_ARTIFACT_ROOT=/tmp/ateliercode-release scripts/release-dev.sh
```

## Pause Points

If `security find-identity -p codesigning` does not show a valid
`Developer ID Application` identity for team `337D6CNU4E`, install or download
the certificate and make sure its private key is available in the release Mac's
Keychain.

If `xcrun notarytool` cannot use the `ateliercode-notary` profile, store or
repair the credentials with `xcrun notarytool store-credentials`.

If `scripts/release-dev.sh --upload` reports that GitHub is not authenticated,
run `gh auth login` locally and retry the upload release.

If Xcode or Keychain displays a GUI approval or password prompt during release,
approve it on the release Mac and rerun the command if needed.

## Laptop Install And Update Check

After upload, verify the release on the laptop:

1. Download the DMG from the GitHub prerelease.
2. Open the DMG.
3. Drag `AtelierCode Dev.app` to `/Applications`.
4. Launch the app and confirm macOS does not show a Gatekeeper warning.
5. Build and upload a newer dev release.
6. Download the newer DMG and replace `/Applications/AtelierCode Dev.app`.
7. Launch again and confirm replacement works.

