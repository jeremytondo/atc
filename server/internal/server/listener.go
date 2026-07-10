package server

import (
	"context"
	"fmt"
	"net"
	"net/http"
)

type ListenerKind string

const (
	ListenerTCP  ListenerKind = "tcp"
	ListenerUnix ListenerKind = "unix"
)

type listenerContextKey struct{}

type ListenerContext struct {
	Kind ListenerKind
}

func ListenerFromContext(ctx context.Context) (ListenerContext, bool) {
	listener, ok := ctx.Value(listenerContextKey{}).(ListenerContext)
	return listener, ok
}

func withListenerBoundary(kind ListenerKind, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), listenerContextKey{}, ListenerContext{Kind: kind})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validateTCPAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid TCP listen address %q: expected host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("invalid TCP listen address %q: missing port", addr)
	}
	return nil
}

func isLoopbackTCPAddr(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
