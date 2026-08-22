package terminal

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

type Config struct {
	Executable   string
	SocketDir    string
	PollInterval time.Duration
	VerifyPasses int
}

type Zmx struct {
	executable   string
	socketDir    string
	pollInterval time.Duration
	verifyPasses int
}

func NewZmx(config Config) (*Zmx, error) {
	executable, err := exec.LookPath(config.Executable)
	if err != nil {
		return nil, fmt.Errorf("find zmx executable %q: %w", config.Executable, err)
	}
	socketDir, err := filepath.Abs(config.SocketDir)
	if err != nil {
		return nil, fmt.Errorf("resolve zmx directory: %w", err)
	}
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return nil, fmt.Errorf("create private zmx directory: %w", err)
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		return nil, fmt.Errorf("protect private zmx directory: %w", err)
	}
	pollInterval := config.PollInterval
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	verifyPasses := config.VerifyPasses
	if verifyPasses <= 0 {
		verifyPasses = 30
	}
	return &Zmx{
		executable:   executable,
		socketDir:    socketDir,
		pollInterval: pollInterval,
		verifyPasses: verifyPasses,
	}, nil
}

func (z *Zmx) Create(ctx context.Context, options CreateOptions) error {
	if len(options.Command) == 0 {
		return errors.New("create session: command is required")
	}
	info, err := os.Stat(options.CWD)
	if err != nil {
		return fmt.Errorf("inspect working directory: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("working directory %s is not a directory", options.CWD)
	}
	existing, err := z.find(ctx, options.Name)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("create %s: session already exists", options.Name)
	}

	command := exec.CommandContext(ctx, z.executable, append([]string{"attach", options.Name}, options.Command...)...)
	command.Dir = options.CWD
	command.Env = z.env(options.Env)
	ptmx, err := pty.StartWithSize(command, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		return fmt.Errorf("start zmx creation client: %w", err)
	}
	tail := newTailBuffer(8 << 10)
	drained := make(chan struct{})
	go func() {
		_, _ = io.Copy(tail, ptmx)
		close(drained)
	}()

	reachable, pollErr := z.poll(ctx, func(sessions []Session) bool {
		for _, session := range sessions {
			if session.Name == options.Name && session.Reachable {
				return true
			}
		}
		return false
	})
	_ = ptmx.Close()
	waitErr := command.Wait()
	select {
	case <-drained:
	case <-time.After(time.Second):
	}
	if pollErr != nil {
		return pollErr
	}
	if reachable {
		return nil
	}
	// The creation client may have exited as the daemon settled. One complete
	// inventory is authoritative; the client exit code is not.
	created, err := z.find(ctx, options.Name)
	if err != nil {
		return err
	}
	if created != nil && created.Reachable {
		return nil
	}
	detail := strings.TrimSpace(tail.String())
	if detail != "" {
		return fmt.Errorf("create %s: session never settled: %s", options.Name, detail)
	}
	return fmt.Errorf("create %s: session never settled: %v", options.Name, waitErr)
}

func (z *Zmx) List(ctx context.Context) ([]Session, error) {
	stdout, stderr, err := z.run(ctx, nil, "list")
	if err != nil {
		return nil, fmt.Errorf("zmx inventory unavailable: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return ParseList(string(stdout)), nil
}

func ParseList(output string) []Session {
	sessions := make([]Session, 0)
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→"))
		if line == "" {
			continue
		}
		fields := make(map[string]string)
		for field := range strings.SplitSeq(line, "\t") {
			key, value, ok := strings.Cut(field, "=")
			if ok && key != "" {
				fields[strings.TrimSpace(key)] = value
			}
		}
		name := fields["name"]
		if name == "" {
			continue
		}
		pid, _ := strconv.Atoi(fields["pid"])
		_, unreachable := fields["err"]
		sessions = append(sessions, Session{Name: name, Reachable: !unreachable, PID: pid})
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].Name < sessions[j].Name })
	return sessions
}

func (z *Zmx) Send(ctx context.Context, name string, input []byte) error {
	session, err := z.requireReachable(ctx, name)
	if err != nil {
		return err
	}
	_, stderr, err := z.run(ctx, bytes.NewReader(input), "send", session.Name)
	if err != nil {
		return fmt.Errorf("send to %s: %w: %s", name, err, strings.TrimSpace(string(stderr)))
	}
	return nil
}

func (z *Zmx) History(ctx context.Context, name string) ([]byte, error) {
	if _, err := z.requireReachable(ctx, name); err != nil {
		return nil, err
	}
	stdout, stderr, err := z.run(ctx, nil, "history", name)
	if err != nil {
		return nil, fmt.Errorf("read history for %s: %w: %s", name, err, strings.TrimSpace(string(stderr)))
	}
	return stdout, nil
}

func (z *Zmx) Attach(ctx context.Context, name string, stdin, stdout *os.File, stderr io.Writer) error {
	before, err := z.requireReachable(ctx, name)
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, z.executable, "attach", name)
	command.Env = z.env(nil)
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		return fmt.Errorf("attach %s: %w", name, err)
	}
	// zmx attach silently creates on a pre-flight race. Daemon PID is the
	// identity: if it changed, stop the phantom replacement and report loss.
	time.Sleep(z.pollInterval)
	after, inventoryErr := z.find(ctx, name)
	if inventoryErr != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return inventoryErr
	}
	if after == nil || !after.Reachable || after.PID != before.PID {
		_ = command.Process.Kill()
		_ = command.Wait()
		if after != nil && after.PID != before.PID {
			_ = z.Kill(context.Background(), name)
		}
		return fmt.Errorf("attach %s: original session disappeared", name)
	}
	if err := command.Wait(); err != nil {
		return fmt.Errorf("attach %s: %w", name, err)
	}
	return nil
}

func (z *Zmx) Kill(ctx context.Context, name string) error {
	existing, err := z.find(ctx, name)
	if err != nil {
		return err
	}
	if existing == nil {
		return nil
	}
	// zmx kill can return before the daemon has actually gone, and its exit
	// status is not proof of existence. A complete inventory is the authority.
	_, _, _ = z.run(ctx, nil, "kill", name)
	gone, err := z.poll(ctx, func(sessions []Session) bool {
		for _, session := range sessions {
			if session.Name == name {
				return false
			}
		}
		return true
	})
	if err != nil {
		return err
	}
	if !gone {
		return fmt.Errorf("kill %s: session still present after %d inventory passes", name, z.verifyPasses)
	}
	return nil
}

func (z *Zmx) requireReachable(ctx context.Context, name string) (*Session, error) {
	session, err := z.find(ctx, name)
	if err != nil {
		return nil, err
	}
	if session == nil {
		return nil, fmt.Errorf("session %s is missing", name)
	}
	if !session.Reachable {
		return nil, fmt.Errorf("session %s is temporarily unreachable", name)
	}
	return session, nil
}

func (z *Zmx) find(ctx context.Context, name string) (*Session, error) {
	sessions, err := z.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, session := range sessions {
		if session.Name == name {
			copy := session
			return &copy, nil
		}
	}
	return nil, nil
}

func (z *Zmx) poll(ctx context.Context, predicate func([]Session) bool) (bool, error) {
	for pass := 0; pass < z.verifyPasses; pass++ {
		sessions, err := z.List(ctx)
		if err != nil {
			return false, err
		}
		if predicate(sessions) {
			return true, nil
		}
		if pass+1 == z.verifyPasses {
			break
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(z.pollInterval):
		}
	}
	return false, nil
}

func (z *Zmx) run(ctx context.Context, stdin io.Reader, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, z.executable, args...)
	command.Env = z.env(nil)
	command.Stdin = stdin
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (z *Zmx) env(overlay map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	delete(values, "ZMX_SESSION")
	delete(values, "ZMX_SESSION_PREFIX")
	values["ZMX_DIR"] = z.socketDir
	values["TERM"] = "xterm-256color"
	for key, value := range overlay {
		values[key] = value
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env
}

type tailBuffer struct {
	mu    sync.Mutex
	limit int
	data  []byte
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (b *tailBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, data...)
	if len(b.data) > b.limit {
		b.data = append([]byte(nil), b.data[len(b.data)-b.limit:]...)
	}
	return len(data), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}
