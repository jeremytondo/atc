# portal POC (IDE-1)

Portal is a small Go application for selecting persistent zmx sessions. It can
run entirely on one machine, or it can run on both a laptop and workstation for
a smoother remote experience.

The central compatibility rule is:

> Portal enhances zmx, but never makes a session inaccessible without Portal.

## Two operating modes

### Local controller (recommended for remote work)

Install Portal on both machines and run this on the laptop:

~~~sh
portal --remote workstation
~~~

The searchable TUI runs locally. Selecting a session starts the system SSH
client and attaches directly to that session on the workstation.

~~~text
local portal TUI
  └─ system ssh
       └─ remote portal attach <session>
            └─ remote zmx session
~~~

This mode distinguishes an intentional detach from a broken connection:

- Ctrl-\ makes zmx and SSH exit normally, returning to the local TUI.
- An SSH connection error causes Portal to reconnect to the same session.
- q from the local TUI exits Portal.
- No autossh installation is required.

Portal delegates authentication, host aliases, proxy jumps, keys, and host-key
checking to the normal system ssh command and its existing configuration. The
Go process does not implement SSH or proxy terminal bytes.

### Hosted manager (remote-only fallback)

When Portal is not installed locally, connect to the copy on the workstation:

~~~sh
ssh -t workstation \
  '$HOME/.local/bin/portal --zmx-bin $HOME/.local/bin/zmx connect'
~~~

This is the original POC architecture. The TUI persists in a special
__portal zmx session on the workstation. Ctrl-\ from another session returns
to that manager. Reconnecting manually starts at the manager rather than
remembering the last viewed session.

## Requirements

Workstation:

- Go 1.24 or newer to build Portal
- zmx
- An SSH server when connecting remotely

Laptop:

- Go 1.24 or newer to build Portal
- OpenSSH client
- zmx is optional unless using Portal for local sessions too

The examples expect both binaries under $HOME/.local/bin.

## Install on the workstation

From a checkout of this repository on the workstation:

~~~sh
cd /path/to/atg-poc
make test
make install
~~~

This creates:

~~~text
$HOME/.local/bin/portal
~~~

Verify Portal and zmx:

~~~sh
$HOME/.local/bin/portal \
  --zmx-bin "$HOME/.local/bin/zmx" \
  doctor
~~~

If zmx is installed somewhere else, substitute its absolute path.

## Install on the laptop

From a checkout of the same repository on the laptop:

~~~sh
cd /path/to/atg-poc
make test
make install
~~~

Add $HOME/.local/bin to PATH if it is not already present:

~~~sh
export PATH="$HOME/.local/bin:$PATH"
~~~

The laptop does not need zmx for remote-controller mode.

## Connect from the laptop

Assuming the SSH host alias is workstation:

~~~sh
portal --remote workstation doctor
portal --remote workstation
~~~

The default remote paths are:

~~~text
.local/bin/portal
.local/bin/zmx
~~~

They are relative to the remote account's home directory. Override them when
needed:

~~~sh
portal \
  --remote workstation \
  --remote-portal /opt/portal/bin/portal \
  --remote-zmx-bin /usr/local/bin/zmx
~~~

An alternate private namespace can also be selected:

~~~sh
portal \
  --remote workstation \
  --remote-zmx-dir /path/to/private/zmx
~~~

## Test reconnection

1. Run portal --remote workstation on the laptop.
2. Create or select a remote session.
3. Run echo $$ and record the remote shell PID.
4. Disable laptop networking for at least 15 seconds.
5. Restore networking.
6. Confirm Portal reconnects directly to the same session.
7. Run echo $$ again and confirm the PID is unchanged.
8. Press Ctrl-\ and confirm this returns to the local TUI rather than
   reconnecting.

Portal uses short SSH keepalive and connection timeouts for this POC. Failed
connections retry with backoff up to eight seconds.

## Development without installation

Hosted mode:

~~~sh
go run ./cmd/portal
~~~

Remote-controller mode, after Portal is installed on the workstation:

~~~sh
go run ./cmd/portal --remote workstation
~~~

Build a repository-local binary:

~~~sh
make build
./bin/portal
~~~

## Session controls

Inside either TUI:

- Type to filter existing managed sessions.
- Press Enter to attach to the selected session.
- Type a new name and press Ctrl-N to create and attach to it.
- Press Ctrl-R to refresh the session inventory.
- Press Ctrl-U to clear the search.
- Press q while the search is empty to quit.

Inside a zmx session:

- Press Ctrl-\ to detach and return to the manager TUI.

## Direct access without Portal

Portal sessions use a private remote namespace:

~~~text
$XDG_STATE_HOME/portal-poc/zmx
# or ~/.local/state/portal-poc/zmx
~~~

List or attach to those sessions using zmx alone:

~~~sh
ssh workstation \
  'ZMX_DIR=$HOME/.local/state/portal-poc/zmx $HOME/.local/bin/zmx list --short'

ssh -t workstation \
  'ZMX_DIR=$HOME/.local/state/portal-poc/zmx $HOME/.local/bin/zmx attach my-session'
~~~

Ordinary zmx sessions continue to use zmx's default namespace and remain
separate.

## Current boundaries

- One interactive client is assumed.
- Remote-controller mode requires compatible Portal protocol versions on both
  machines and reports a clear mismatch otherwise.
- If the local Portal process is closed, remote zmx sessions survive. A new
  Portal invocation returns to the local TUI rather than automatically opening
  the previously selected session.
- Session creation starts the user's default zmx shell. Agent-specific commands
  and metadata are not modeled yet.
- The hosted manager has been exercised against zmx 0.6.0.
