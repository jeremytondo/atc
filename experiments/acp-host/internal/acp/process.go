package acp

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type Command struct {
	Path string
	Args []string
	Dir  string
	Env  []string
}

type Process struct {
	command    *exec.Cmd
	connection *Connection
	wait       chan error
	stopOnce   sync.Once
}

func Launch(command Command, handler Handler, stderr io.Writer) (*Process, error) {
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = command.Dir
	cmd.Env = append(os.Environ(), command.Env...)
	cmd.Stderr = stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open agent stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open agent stdout: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start agent: %w", err)
	}

	connection := NewConnection(stdout, stdin, handler)
	process := &Process{command: cmd, connection: connection, wait: make(chan error, 1)}
	connection.Start()
	go func() {
		process.wait <- cmd.Wait()
	}()
	return process, nil
}

func (p *Process) Connection() *Connection {
	return p.connection
}

func (p *Process) PID() int {
	return p.command.Process.Pid
}

func (p *Process) Stop(ctx context.Context) error {
	var closeErr error
	p.stopOnce.Do(func() {
		closeErr = p.connection.Close()
	})
	select {
	case err := <-p.wait:
		if err != nil && closeErr == nil {
			return err
		}
		return closeErr
	case <-ctx.Done():
		if err := p.command.Process.Kill(); err != nil {
			return fmt.Errorf("agent did not exit and kill failed: %w", err)
		}
		select {
		case <-p.wait:
		case <-time.After(time.Second):
		}
		return ctx.Err()
	}
}
