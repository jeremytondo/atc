# Linear Integration

Linear sends work into ATC: an explicit `@atc` mention with a question on
an issue creates a Linear Agent Session, ATC starts one T3 Code
conversation (Codex, `gpt-5.6-sol`, high reasoning effort) in one
configured Project, adds the conversation's T3 links to the session, and
posts the conversation's final response back when its first turn ends.
Everything else — answering the agent's questions, approvals, follow-ups,
stopping — happens in T3 Code; Linear is told where to go.

Setup is manual. It needs a Linear OAuth application installed as an app
actor, ATC's public webhook endpoint, and one setup file.

## 1. Expose ATC's webhook endpoint

Enable webhook ingress (`webhooks = true` in `config.toml`, or
`atc server start --webhooks`) and finish the Funnel approval that
`atc server status` reports. The Linear route is
`<webhook URL>/linear`, printed as `webhook route (linear)` in the same
status output. The endpoint must be `ready` before Linear can deliver;
that readiness and a working Linear app are separate prerequisites.

## 2. Create the Linear OAuth application

In Linear: Settings → API → OAuth applications → New. Any name and icon;
this becomes the `@atc` app user. Record the client id and client secret.

- Redirect URI: something you control that can receive a browser
  redirect, e.g. `http://localhost:9999/callback`. Nothing has to serve
  it; the authorization code is read off the address bar.
- Webhooks: enable, set the URL to `<webhook URL>/linear`, and select
  **Agent session events**. Record the webhook signing secret.

## 3. Install the app as an app actor and obtain tokens

Open the authorization URL in a browser as a workspace admin, with
`actor=app` so the grant is the app's own installation:

```text
https://linear.app/oauth/authorize?client_id=<client id>&redirect_uri=<redirect URI>&response_type=code&scope=read,write,app:assignable,app:mentionable&actor=app
```

After approving, the browser lands on the redirect URI with `?code=…`.
Exchange it within a few minutes:

```sh
curl -s https://api.linear.app/oauth/token \
  -d grant_type=authorization_code \
  -d code=<code> \
  -d redirect_uri=<redirect URI> \
  -d client_id=<client id> \
  -d client_secret=<client secret>
```

The answer carries `access_token`, `refresh_token`, and `expires_in`
(access tokens last 24 hours; ATC renews them unattended with the refresh
token and rewrites the setup file). Find the workspace id the token acts
in:

```sh
curl -s https://api.linear.app/graphql \
  -H "Authorization: Bearer <access token>" \
  -H "Content-Type: application/json" \
  -d '{"query":"{ viewer { id name organization { id name } } }"}'
```

## 4. Register the Project in ATC and in T3 Code

The Project every session runs in must exist in ATC (`atc project list`)
and be registered in T3 Code at the same directory. Note the ATC project
id (`proj-…`).

## 5. Write the setup file

`~/.local/share/atc/linear.json` (`$XDG_DATA_HOME/atc/linear.json`),
mode 0600:

```json
{
  "organization_id": "<viewer.organization.id>",
  "client_id": "<client id>",
  "client_secret": "<client secret>",
  "webhook_signing_secret": "<signing secret>",
  "access_token": "<access token>",
  "refresh_token": "<refresh token>",
  "project_id": "proj-xxxxx"
}
```

Unknown keys are refused. The server picks up the file within about
thirty seconds without a restart, and rewrites only the token fields when
it renews them.

## 6. Check

`atc integration get linear` reports the state: `unavailable` with the
reason while the file is missing or incomplete, `auth_failed` with the
operator action when Linear refuses the token, `connecting` while Linear
is unreachable, and `connected` naming the app user and workspace. The
detail also counts open sessions and owed Linear updates and names the
webhook route with its readiness. `atc server status` shows the ingress
side.

Then mention `@atc` in an issue comment with an explicitly read-only
question against the Project. Within seconds the session shows an
acknowledgement, then the T3 links when T3 Code provides them; the answer
follows when the run ends.

## Behavior worth knowing

- Only an explicit mention in an issue comment starts work. Delegating
  the issue, or a mention without context, gets an explanation and no
  run.
- Follow-up messages and stop signals in the session get a fixed
  explanation pointing to T3 Code; they never reach the agent and never
  end ATC's tracking.
- Tracking has no duration limit and survives server restarts. A server
  that restarts mid-start, or a T3 Code that never answers the start,
  never leads to a second conversation: ATC says the start is uncertain
  and watches what it recorded. T3 Code being disconnected defers the
  start until it is back.
- The answer is the exact turn's final response. If a newer turn replaces
  it in T3, or the response cannot be recovered, Linear gets an
  explanation with the links instead of a substitute.
