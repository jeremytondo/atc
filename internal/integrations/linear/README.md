# Linear Integration

Linear sends work into ATC: an explicit `@atc` mention with a question on
an issue creates a Linear Agent Session, ATC starts one T3 Code
conversation (Codex, `gpt-5.6-sol`, high reasoning effort) in one
configured Project, adds the conversation's T3 links to the session, and
posts the conversation's final response back when its first turn ends.
The session stays bound to that conversation: a follow-up message in the
session continues it, the agent's questions and approval requests appear
in the session with the choices offered, a choice or a reply in words
answers them, and Linear's stop stops the work. Every result is reported
against the exact message, question, or stop it belongs to.

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
detail also counts open sessions, submissions in flight, and owed Linear
updates, and names the webhook route with its readiness. `atc server status` shows the ingress
side.

Then mention `@atc` in an issue comment with an explicitly read-only
question against the Project. Within seconds the session shows an
acknowledgement, then the T3 links when T3 Code provides them; the answer
follows when the run ends. Reply in the session to continue.

## Behavior worth knowing

- Only an explicit mention in an issue comment starts work. Delegating
  the issue, or a mention without context, gets an explanation and no
  run.
- A message in the session continues the same conversation under its
  current agent, model, and settings, exactly as `atc thread send` does:
  it starts the next turn once the current one is over, and while a
  submitted turn has not started yet a further message is refused and
  says so — nothing is queued or reinterpreted. Each turn's final
  response is posted against the message that started it.
- The agent's questions appear as an elicitation listing the questions
  and choices, with Linear's select options. Picking an option answers
  that one question with that exact choice; replying in words sends the
  text verbatim as a reply to the open question — the agent reads it
  against its questions; ATC neither classifies it nor fills in the rest.
  A reply to a question that is no longer open is not sent, and not
  turned into a message either; the session says so.
- Approval requests appear the same way with the decisions offered. Only
  picking one decides — text such as "yes, go ahead", or the decision's
  label typed out, is a message and approves nothing. A decision on a
  request already resolved elsewhere is refused and reported as such.
- Linear's stop goes through ATC's stop: the session hears that the stop
  was sent, then what T3 Code confirmed — stopped, already finished, or
  refused. While the stop is being confirmed, new messages, answers, and
  decisions are refused rather than held. The conversation is kept, and
  a later message continues it.
- Everything is durable and survives server restarts: the session's
  binding, every submission with its target and outcome, and every
  request presented with the options offered. A submission the program
  never answered is retried under the same identity, never sent twice;
  one the program could not take because T3 Code was away waits with a
  bounded backoff and goes on T3 Code's return. A server that restarts
  mid-start, or a T3 Code that never answers the start, never leads to a
  second conversation.
- Results are exact: a turn's final response, a failure with its detail,
  or an explanation with the links when a newer turn replaced the tracked
  one, T3 dropped the conversation, or the response could not be
  recovered — never a substitute. Turns started from T3 Code itself are
  not mirrored.
