package portal

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
)

const (
	managerSession  = "__portal"
	protocolVersion = 1
)

type App struct {
	in     *os.File
	out    *os.File
	errOut io.Writer
	env    []string

	zmxBin string
	zmxDir string

	remoteHost   string
	sshBin       string
	remotePortal string
	remoteZMXBin string
	remoteZMXDir string
}

func New(in, out *os.File, errOut io.Writer) (*App, error) {
	return &App{
		in:           in,
		out:          out,
		errOut:       errOut,
		env:          os.Environ(),
		zmxBin:       lookPathOrName("zmx"),
		zmxDir:       defaultZMXDir(),
		sshBin:       lookPathOrName("ssh"),
		remotePortal: ".local/bin/portal",
		remoteZMXBin: ".local/bin/zmx",
	}, nil
}

func lookPathOrName(name string) string {
	path, err := exec.LookPath(name)
	if err != nil {
		return name
	}
	return path
}

func (a *App) Run(args []string) error {
	flags := flag.NewFlagSet("portal", flag.ContinueOnError)
	flags.SetOutput(a.errOut)
	flags.StringVar(&a.zmxDir, "zmx-dir", a.zmxDir, "private local zmx socket directory")
	flags.StringVar(&a.zmxBin, "zmx-bin", a.zmxBin, "path to local zmx")
	flags.StringVar(&a.remoteHost, "remote", "", "SSH host or alias to control")
	flags.StringVar(&a.sshBin, "ssh-bin", a.sshBin, "path to the local ssh executable")
	flags.StringVar(&a.remotePortal, "remote-portal", a.remotePortal, "path to portal on the remote host")
	flags.StringVar(&a.remoteZMXBin, "remote-zmx-bin", a.remoteZMXBin, "path to zmx on the remote host")
	flags.StringVar(&a.remoteZMXDir, "remote-zmx-dir", "", "private ZMX_DIR on the remote host")
	if err := flags.Parse(args); err != nil {
		return err
	}

	rest := flags.Args()
	command := "connect"
	if len(rest) > 0 {
		command = rest[0]
		rest = rest[1:]
	}

	if a.remoteHost != "" {
		return a.runRemoteCommand(command, rest)
	}

	switch command {
	case "connect":
		if len(rest) != 0 {
			return errors.New("connect does not accept arguments")
		}
		return a.connect()
	case "attach":
		if len(rest) != 1 {
			return errors.New("attach requires exactly one session name")
		}
		return a.attachIndependent(rest[0])
	case "list", "ls":
		if len(rest) == 1 && rest[0] == "--json" {
			return a.printInventoryJSON()
		}
		if len(rest) != 0 {
			return errors.New("list accepts only --json")
		}
		return a.printSessions()
	case "doctor":
		if len(rest) != 0 {
			return errors.New("doctor does not accept arguments")
		}
		return a.doctor()
	case "protocol-version":
		if len(rest) != 0 {
			return errors.New("protocol-version does not accept arguments")
		}
		fmt.Fprintln(a.out, protocolVersion)
		return nil
	case "tui":
		if len(rest) != 0 {
			return errors.New("tui does not accept arguments")
		}
		return a.tui()
	case "help", "-h", "--help":
		a.usage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try portal help)", command)
	}
}

func (a *App) runRemoteCommand(command string, args []string) error {
	backend := newRemoteBackend(a)
	switch command {
	case "connect":
		if len(args) != 0 {
			return errors.New("remote connect does not accept arguments")
		}
		return a.remoteConnect(backend)
	case "list", "ls":
		if len(args) != 0 {
			return errors.New("remote list does not accept arguments")
		}
		sessions, err := backend.ListSessions()
		if err != nil {
			return err
		}
		for _, session := range sessions {
			fmt.Fprintln(a.out, session)
		}
		return nil
	case "doctor":
		if len(args) != 0 {
			return errors.New("remote doctor does not accept arguments")
		}
		return backend.Doctor()
	case "help", "-h", "--help":
		a.usage()
		return nil
	default:
		return fmt.Errorf("command %q is not available with --remote", command)
	}
}

func (a *App) usage() {
	fmt.Fprintln(a.out, "portal is a zmx session switcher for local and remote hosts.")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Usage:")
	fmt.Fprintln(a.out, "  portal [flags] connect                   run the hosted manager locally")
	fmt.Fprintln(a.out, "  portal --remote HOST [flags] connect     run a local TUI for a remote host")
	fmt.Fprintln(a.out, "  portal [flags] list                      list hosted sessions")
	fmt.Fprintln(a.out, "  portal --remote HOST [flags] list        list sessions on a remote host")
	fmt.Fprintln(a.out, "  portal [flags] doctor                    show local zmx configuration")
	fmt.Fprintln(a.out, "  portal --remote HOST [flags] doctor      verify the remote installation")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Remote controller flags:")
	fmt.Fprintln(a.out, "  --remote HOST            SSH host or alias")
	fmt.Fprintln(a.out, "  --ssh-bin PATH           local ssh executable")
	fmt.Fprintln(a.out, "  --remote-portal PATH     remote portal path (default .local/bin/portal)")
	fmt.Fprintln(a.out, "  --remote-zmx-bin PATH    remote zmx path (default .local/bin/zmx)")
	fmt.Fprintln(a.out, "  --remote-zmx-dir PATH    optional remote private zmx namespace override")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "Hosted mode flags:")
	fmt.Fprintln(a.out, "  --zmx-dir PATH           private local zmx namespace")
	fmt.Fprintln(a.out, "  --zmx-bin PATH           local zmx executable")
	fmt.Fprintln(a.out)
	fmt.Fprintln(a.out, "With no command, portal runs connect.")
}
