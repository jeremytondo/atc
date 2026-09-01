# ATC-287 TUI handoff and SSH recovery evidence

Tested on 2026-08-31 with Go 1.26.7, Bubble Tea 2.0.9, zmx 0.6.0,
and the repository's OpenSSH client and server.

This work lives in production packages because ATC-286 will reuse the
connection and handoff seams. The hidden `atc __tui-proof` command is only the
small proof screen; it is not the product launcher.

## Result

Do not adopt the proof's SSH topology as implemented. Bubble Tea v2's
interactive-process handoff remains promising, but a real connection from the
developer's laptop exposed an ownership flaw in the remote control design
before the physical sleep/wake test could begin.

The proof assumed that its `ssh -N` child would remain alive for the lifetime
of the API forward. The developer's ordinary `workstation` target uses
`ControlMaster auto`, a shared `ControlPath`, and `ControlPersist 10m`. OpenSSH
installed the forward on that existing master and let the proof's child exit
successfully. ATC loaded the first authenticated terminal snapshot, then
treated the child exit as control loss and entered a reconnect loop. Requiring
users to disable a useful SSH configuration would not be a seamless remote
experience.

When TUI work resumes, replace the process-topology requirement with these
outcome requirements:

- ATC owns and can supervise every remote resource it creates.
- ATC neither depends on nor mutates the user's shared SSH control master.
- ATC uses one private, launcher-owned OpenSSH master per remote launcher and
  multiplexes bootstrap, authenticated API forwarding, and interactive
  attachment as channels over it.
- Ordinary OpenSSH target configuration continues to supply host, user,
  identity, proxy, host-verification, and keepalive behavior. ATC overrides
  only settings that would transfer connection ownership or force a TTY on
  control commands.
- Connection loss has deterministic cleanup and recovery: restore the TUI,
  start a new owned master, load a complete snapshot, retain stable-ID
  selection, and retry only the interrupted terminal.

The intended resumed topology is:

1. Create a private mode-0700 directory and SSH control socket.
2. Start a foreground, ATC-owned OpenSSH master for the ordinary config target.
3. Run `atc __remote prepare` and install the private API forward through that
   master.
4. Run `ssh -S <private-socket> -tt ...` attachments through the same master.
5. On transport loss, treat both channels as one failed owned connection and
   rebuild it. On exit, close only that master and its private state.

This retains the useful mechanics the proof established:

- Bubble Tea runs local zmx or remote interactive SSH through `ExecProcess`,
  releasing the renderer and terminal modes before the child starts and
  restoring both afterward.
- A short `atc __remote prepare` command starts or finds the already-installed
  remote server and returns its port, token, and exact version through the
  encrypted SSH channel. The token stays in memory and is not added to local
  configuration.
- OpenSSH keepalives bound failure detection to roughly 15 seconds. The TUI
  retains the last snapshot and stable-ID selection, reconnects with a
  1-to-30-second backoff, reloads a complete snapshot, and retries the same
  terminal only when interactive SSH reports transport exit 255.

The local handoff and synthetic recovery evidence pass, but the common SSH
configuration test fails. The physical sleep/wake test was not reached.
ATC-286 remains blocked, and the work is tabled for later redesign. The full
proof source and evidence are preserved by the Git tag
`atc-287-tui-handoff-proof-2026-09-01`; PR #269 is intentionally not merged.

## Observed evidence

| Case | Result | Observation |
| --- | --- | --- |
| Local renderer handoff | Pass | A real Bubble Tea program released its alternate screen to a real zmx-backed shell and restored the same model after detach. |
| Local input and resize | Pass | The attached shell printed `ATC287_ATTACH_OK`; a resize from 100x31 to 111x37 reached the active PTY, and the restored renderer reported 111x37. |
| Selection and refresh | Pass | The same terminal ID remained selected after detach and a complete snapshot refresh; a forced 112x38 repaint verified the retained row. |
| Real OpenSSH control path | Pass | A private localhost `sshd` accepted an ordinary config target; ATC carried a bearer-authenticated `/v1/terminals` request over its private Unix forward. |
| Real remote attachment | Pass | A separate `ssh -tt` process attached the caller's PTY to a private remote zmx shell, printed `ATC287_REMOTE_OK`, propagated resize, detached normally, and restored the TUI. |
| Control failure and reconnect | Pass | Killing the exact launcher-owned `ssh -N` PID left the screen alive and stale, then created a new forward after bounded backoff, loaded a fresh snapshot, and retained the selected terminal. |
| Same-terminal attachment retry | Pass (deterministic) | Exit 255 records the interrupted terminal ID; a successful reconnect snapshot reissues attachment only when that same terminal is still running. Escape invalidates the retry generation. |
| Stable remote failures | Pass (deterministic) | Missing remote ATC (exit 127), version mismatch, malformed/oversized bootstrap output, and invalid targets stop blind retry. |
| Cleanup | Pass | Tests removed private forward directories and stopped only captured server, SSH, API, TUI, and terminal resources. Race-enabled tests cover concurrent process completion and cleanup. |
| Ordinary shared ControlMaster config | Fail | The remote server started and an authenticated snapshot loaded, but `ssh -N` delegated its forward to the user's persistent master and exited successfully. ATC then reported `SSH control forward exited` and reconnected indefinitely. |
| Physical laptop sleep/wake | Not reached | The common-config failure happened before sleep/wake. This remains a future acceptance gate for the redesigned owned-master topology. |

The real-path runs used private ATC/zmx/SSH state. They did not read or mutate
the developer's normal zmx namespace, SSH configuration, or supervised ATC
server. One zmx-specific observation is worth retaining: with zmx 0.6.0,
`zmx detach` from a different client returned success but did not release the
active attach client; interactive `Ctrl+\` remained the reliable detach path.

## Final laptop observation

The failing target used this relevant configuration:

```sshconfig
Host workstation
    ControlMaster auto
    ControlPath ~/.ssh/cm-%C
    ControlPersist 10m
    ServerAliveInterval 5
    ServerAliveCountMax 2
    RequestTTY yes
```

The proof displayed the remote terminal list, proving that remote server
startup, version agreement, bearer authentication, forwarding, and the first
snapshot had succeeded. It then immediately displayed a stale snapshot in
`state=reconnecting` with `SSH control connection lost: SSH control forward
exited`. This is a design failure, not an operator setup failure. A final
implementation must work with this configuration unchanged.

## Automated checks

Run the complete repository checks:

```sh
mise check
```

The focused tests are:

```sh
go test -race ./internal/cli ./internal/remote ./internal/tui ./cmd/atc
```

They cover exact OpenSSH arguments, keepalives, private forwarding state,
bearer and version headers, stable error classification, child cleanup,
selection by terminal ID, stale-state mutation refusal, post-attach
remeasurement, reconnect generations, cancellation, and same-terminal retry.

## Exact physical sleep/wake procedure

Use a dedicated test account or disposable remote host so the remote user's
supervised ATC server and zmx namespace are explicitly scoped to this test.
Install the same ATC build and zmx 0.6.0 or newer on both machines. Configure
an ordinary OpenSSH target, shown here as `atc287-host`, and first verify it is
non-interactive:

```sh
ssh atc287-host 'atc version && zmx version'
./bin/atc version
```

The ATC versions must match exactly. On the remote test account, create one
project and one detached terminal, recording both printed IDs:

```sh
ssh -t atc287-host 'atc project create /absolute/remote/test/project'
ssh -t atc287-host 'atc terminal create --project proj-xxxxx --name ATC-287 --detach'
```

Start the proof locally:

```sh
./bin/atc __tui-proof --remote atc287-host
```

Perform these checks in order:

1. Select the recorded terminal and press Enter. Run
   `printf 'ATC287_SLEEP_OK\n'`, resize the terminal in both dimensions, and
   detach with `Ctrl+\`. Verify the same row is selected and the header shows
   the new dimensions.
2. Press `r`. Verify the same row stays selected after refresh.
3. While on the launcher, disable the client's network for at least 20
   seconds. Verify the last snapshot remains visible as stale and navigation
   still works. Restore the network and verify the launcher reconnects without
   losing selection.
4. Attach again, run `printf 'BEFORE_SLEEP\n'`, and close the laptop lid for at
   least 30 seconds. Wake it and restore the network if necessary. The failed
   interactive SSH process should return the launcher to reconnecting state;
   once control recovers, ATC should retry the same terminal automatically.
   Run `printf 'AFTER_WAKE\n'` in the recovered shell to prove continuity,
   then detach normally.
5. Repeat step 4 but press Escape while the launcher is waiting to retry.
   Verify it returns to the stale launcher and does not reattach. Press `r` to
   reconnect control manually.
6. Quit with `q`, then repeat once using `Ctrl+C` while connected and once
   during reconnect. Compare `ps` output captured before and after; no
   launcher-owned `ssh -N` or `ssh -tt` child should remain. Confirm its
   `/tmp/atc-ssh-*` directory is gone. Do not kill by process name.

Clean up only the recorded resources:

```sh
ssh atc287-host 'atc terminal delete term-xxxxx'
ssh atc287-host 'atc project delete proj-xxxxx'
```

If the dedicated account exists only for this test, `atc server uninstall`
under that account removes its service registration without deleting data.

## Known failure modes

- OpenSSH must be able to authenticate from a non-interactive control command;
  complete first-use host-key and key-agent setup before starting the TUI.
- A missing remote `atc` exits as stable configuration failure with an install
  remedy. ATC does not install or upgrade the remote binary.
- Exact version mismatch is stable and names both versions.
- Control snapshots time out after 10 seconds. The last complete state remains
  visible and read-only while reconnecting.
- Remote command failures other than SSH's transport status 255 do not trigger
  same-terminal retry, preventing a failed or refused attach from looping.
- A terminal that is absent or no longer running in the reconnect snapshot is
  not retried.
