# ATC-284 next-step probes

Run from the repository root. The helper observes the existing shared server;
it never starts, stops, or configures it.

## 1. Blocking test: launch then observe

The `bind` command connects the observer, launches plain `codex`, and watches
the first five seconds for `thread/started` events with the launch cwd. Wait six
seconds before entering a prompt. Exit with `/exit`, then press Enter again.
The result is printed and saved under `/tmp/atc-284-bind-*.log`.

### A. No prompt

```sh
./scripts/atc-284 bind "$PWD"
```

Wait six seconds without prompting, then exit. Expect exactly one candidate and
`PASS`.

### B. Two launches in the same cwd

Run the command in Terminal 1. After six seconds, leave that TUI open and run
the same command in Terminal 2. After another six seconds, exit both TUIs.

```sh
./scripts/atc-284 bind "$PWD"
```

Expect one distinct candidate and `PASS` from each launch. Starting the second
after the first observation window models ATC's serialized pending launches.

### C. Launch while another thread is active

Start a turn in an existing TUI that lasts at least ten seconds. While its
status is active, run this in another terminal:

```sh
./scripts/atc-284 bind "$PWD"
```

Wait six seconds, then exit the new TUI. `PASS` is a clean binding. Multiple
matching candidates or no candidate is an expected fail-closed result; any
incorrect single candidate is a cross-binding failure.

Collect all reports:

```sh
sed -n '1,120p' /tmp/atc-284-bind-*.log
```

## 2. Re-check the iOS thread

Check the previously failing iOS-originated thread:

```sh
./scripts/atc-284 rollout 01a05747-6bea-7bd1-96ed-666b6bfbb807
```

If a file is printed, wait until the thread is idle and retry:

```sh
codex resume 01a05747-6bea-7bd1-96ed-666b6bfbb807
```

This is a scope check and does not block ATC-284.

## 3. Cold start after reboot

Before opening Desktop or another Codex client:

```sh
codex app-server --listen unix://
```

In a second terminal:

```sh
cd ~/Projects/ATC/atc
./scripts/atc-284 doctor
codex
```

Then reconnect Desktop and iOS. Stop only the manually started server with
Ctrl-C when finished.

## Other commands

```sh
./scripts/atc-284 doctor
./scripts/atc-284 listen
./scripts/atc-284 create "$PWD"
```

Use `--socket PATH` before a server command to target an isolated socket.
