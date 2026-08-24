package main

import (
	"context"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"
)

func TestHTTPStartsBeforeSlowProviderRecoveryCompletes(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listener := newBlockingListener()
	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	serviceClosed := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- serveHTTP(
			ctx,
			listener,
			&http.Server{Handler: http.NewServeMux()},
			func(context.Context) {
				close(recoveryStarted)
				<-releaseRecovery
			},
			func(context.Context) error {
				close(serviceClosed)
				return nil
			},
		)
	}()

	waitForSignal(t, recoveryStarted, "provider recovery did not start")
	waitForSignal(t, listener.accepted, "HTTP server did not accept while provider recovery was blocked")
	close(releaseRecovery)
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP server did not stop")
	}
	waitForSignal(t, serviceClosed, "service was not closed")
}

func waitForSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

type blockingListener struct {
	accepted  chan struct{}
	closed    chan struct{}
	acceptOne sync.Once
	closeOne  sync.Once
}

func newBlockingListener() *blockingListener {
	return &blockingListener{accepted: make(chan struct{}), closed: make(chan struct{})}
}

func (l *blockingListener) Accept() (net.Conn, error) {
	l.acceptOne.Do(func() { close(l.accepted) })
	<-l.closed
	return nil, net.ErrClosed
}

func (l *blockingListener) Close() error {
	l.closeOne.Do(func() { close(l.closed) })
	return nil
}

func (l *blockingListener) Addr() net.Addr { return testAddress("127.0.0.1:0") }

type testAddress string

func (a testAddress) Network() string { return "tcp" }
func (a testAddress) String() string  { return string(a) }
