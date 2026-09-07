# ATC-309 provider probe: replies to multi-question requests

Status: the Codex behavior ATC-309's shared answer contract rests on is
established live, on 2026-09-07, against the installed Codex CLI
(`codex-cli 0.153.4`, model `gpt-5.6-sol`, plan collaboration mode, an
ephemeral thread in an empty temporary directory, read-only sandbox, no
approvals). `codex-probe.py` drives `codex app-server` over stdio and
answers the `item/tool/requestUserInput` server request the way ATC's T3
Code translation answers it; `reply.json` and `partial.json` are the
recorded outcomes.

## Question

T3 Code forwards the answer map of a `thread.user-input.respond` command to
Codex without checking that every question is answered (T3's own form
does), and Codex's `request_user_input` handler hands the map to the model
verbatim as the tool's result. Source reading established that a partial
map is accepted; it did not establish what the agent makes of one. ATC-309
sends a conversational reply as the custom answer to the request's first
question and nothing for the others, and a choice as the answer to its
question alone. Does the agent read those faithfully — which questions were
answered, the text as written, a change of direction honored?

## Method

The prompt asks the model to call `request_user_input` once with three
questions (color, tool, tests) and then report, in a fixed three-line
format, which question ids it received answers for, the verbatim text,
and its plan. The probe answers the request with:

- `reply`: `{ "color": { "answers": ["Blue is fine. Actually, forget the
  tooling question - and please skip the tests entirely; instead tell me
  in one sentence what you would do first."] } }` — a reply in prose that
  answers one question, drops another, and reverses the third, keyed to
  the first question as ATC keys a reply.
- `partial`: `{ "tool": { "answers": ["make"] } }` — one question's choice,
  nothing for the others, as ATC sends a choice picked in Linear.

Codex adds a free-form "Other" to every question (`isOther: true`) and
suffixes its recommended option with "(Recommended)", so the options the
agent offers differ from the ids and labels the prompt asked for; the ids
are what the map is keyed by.

## Findings

- A partial map reaches the agent as such. In both cases the agent
  reported exactly the questions the map named (`ANSWERED: color`,
  `ANSWERED: tool`) and treated the others as unanswered; it did not
  invent answers for them or ask again.
- The reply text reaches the agent verbatim (`TEXT:` echoed the sentence
  as sent) and the agent honored the change of direction: its plan was to
  set the color and neither choose a build tool nor run tests.
- A single choice keyed to its own question is read as that choice
  (`PLAN: I would use make for the build.`).
- `request_user_input` is only available in plan mode on this Codex
  version; in the default mode the model refuses to call it. That is the
  provider's rule, not T3's or ATC's, and it bounds when a Linear user
  will see questions at all.

These findings support ATC-309's translation — a reply keyed to the first
question, a choice keyed to its question, no duplication across questions
— as faithful to what the agent actually receives. They do not establish
anything about other providers (Claude Code through T3 answers the same
map shape through a different adapter) and were not run through T3 Code
itself; T3's forwarding is established from source
(`CodexSessionRuntime.respondToUserInput`) and covered by the package
tests under `internal/integrations/t3code`.

## Running it again

```sh
python3 experiments/atc-309/codex-probe.py reply
python3 experiments/atc-309/codex-probe.py partial
```

It spends one model turn per case against the account `codex` is logged
in with, writes its wire log to `/tmp/atc-309/<case>.jsonl`, and touches
nothing else.
