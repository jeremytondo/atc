package t3code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Pairing is zero-touch (ATC-285): with no stored session, or one T3
// rejects, ATC mints a five-minute pairing grant through T3's own CLI —
// the one thing the CLI is used for — exchanges it at T3's OAuth token
// endpoint for a session scoped to exactly orchestration:read, and
// persists that session 0600 beside ATC's own auth token. The grant is
// one-use, consumed by the exchange; the session outlives it, so the
// previous ATC session is revoked before a new one is persisted.

const (
	// sessionLabel names ATC's session in T3's client list; it is how the
	// session id is found after the exchange, and what a user sees under
	// T3's connections.
	sessionLabel = "atc"
	scope        = "orchestration:read"
)

// session is the persisted credential.
type session struct {
	Origin    string `json:"origin"`
	Token     string `json:"token"`
	Label     string `json:"label"`
	SessionID string `json:"sessionId"`
}

// authError marks a pairing or exchange that failed for a reason a retry
// will not fix by itself: the Integration reports auth_failed and waits
// before trying again.
type authError struct{ err error }

func (e *authError) Error() string { return e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

func authErrorf(format string, args ...any) error {
	return &authError{err: fmt.Errorf(format, args...)}
}

func loadSession(path string) (*session, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s session
	if err := json.Unmarshal(data, &s); err != nil || s.Origin == "" || s.Token == "" {
		return nil, fmt.Errorf("session file %s does not hold a session", path)
	}
	return &s, nil
}

// saveSession writes the session 0600 by atomic replace, so a crash or a
// concurrent reader never sees a partial credential.
func saveSession(path string, s *session) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(temp)
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return nil
}

// runNodeCLI is the production CLI runner: the versioned T3 entrypoint
// under the T3 home, run with node against that home. A missing CLI or
// node is an auth failure — pairing is the only caller.
func runNodeCLI(home string) func(ctx context.Context, args ...string) ([]byte, error) {
	return func(ctx context.Context, args ...string) ([]byte, error) {
		bin, err := cliPath(home)
		if err != nil {
			return nil, &authError{err: err}
		}
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, authErrorf("node not found on the server's PATH; T3 Code's CLI needs it to pair")
		}
		cmd := exec.CommandContext(ctx, node, append([]string{bin}, append(args, "--base-dir", home)...)...)
		cmd.Env = append(os.Environ(), "NODE_NO_WARNINGS=1")
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			detail := strings.TrimSpace(stderr.String())
			if len(detail) > 200 {
				detail = detail[:200] + "…"
			}
			return nil, authErrorf("t3 %s: %w: %s", strings.Join(args, " "), err, detail)
		}
		return out, nil
	}
}

// pair mints and exchanges a grant for a fresh session at origin.
func (o *Observer) pair(ctx context.Context, origin string) (*session, error) {
	out, err := o.runCLI(ctx, "auth", "pairing", "create", "--ttl", "5m", "--label", sessionLabel, "--json")
	if err != nil {
		var auth *authError
		if errors.As(err, &auth) {
			return nil, err
		}
		return nil, &authError{err: err}
	}
	var grant struct {
		Credential string `json:"credential"`
	}
	if err := json.Unmarshal(out, &grant); err != nil || grant.Credential == "" {
		return nil, authErrorf("t3 auth pairing create answered without a credential")
	}

	form := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":        {grant.Credential},
		"subject_token_type":   {"urn:t3:params:oauth:token-type:environment-bootstrap"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"scope":                {scope},
		"client_label":         {sessionLabel},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/oauth/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var token struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
	}
	if err := doJSON(o.httpClient, req, &token); err != nil {
		var status *httpError
		if errors.As(err, &status) && status.status < 500 {
			return nil, authErrorf("T3 Code refused the pairing exchange: %w", err)
		}
		// A T3 that cannot answer right now is not a pairing failure.
		return nil, fmt.Errorf("pairing exchange: %w", err)
	}
	if token.AccessToken == "" {
		return nil, authErrorf("T3 Code's pairing exchange answered without a token")
	}
	if token.Scope != scope {
		return nil, authErrorf("T3 Code granted scope %q, not %q", token.Scope, scope)
	}
	s := &session{Origin: origin, Token: token.AccessToken, Label: sessionLabel}
	// The exchange does not name the session it issued; T3's client list
	// does, by label. Best effort: without the id the session still works,
	// it just cannot be revoked at the next pairing.
	if id, err := o.findSession(ctx); err != nil {
		o.logger.Warn("t3code: session id not recorded", "error", err)
	} else {
		s.SessionID = id
	}
	return s, nil
}

// findSession returns the id of the newest session carrying ATC's label.
func (o *Observer) findSession(ctx context.Context) (string, error) {
	out, err := o.runCLI(ctx, "auth", "session", "list", "--json")
	if err != nil {
		return "", err
	}
	var sessions []struct {
		SessionID string `json:"sessionId"`
		IssuedAt  string `json:"issuedAt"`
		Client    struct {
			Label string `json:"label"`
		} `json:"client"`
	}
	if err := json.Unmarshal(out, &sessions); err != nil {
		return "", fmt.Errorf("t3 auth session list: %w", err)
	}
	var id, issued string
	for _, s := range sessions {
		if s.Client.Label == sessionLabel && s.IssuedAt >= issued {
			id, issued = s.SessionID, s.IssuedAt
		}
	}
	if id == "" {
		return "", errors.New("t3 auth session list shows no session labeled " + sessionLabel)
	}
	return id, nil
}

// revoke ends a stored session in T3, best effort: sessions must not
// accumulate there across re-pairings, but a failure here never blocks
// the new pairing.
func (o *Observer) revoke(ctx context.Context, s *session) {
	if s == nil || s.SessionID == "" {
		return
	}
	if _, err := o.runCLI(ctx, "auth", "session", "revoke", s.SessionID); err != nil {
		o.logger.Warn("t3code: revoking the previous session", "session", s.SessionID, "error", err)
	}
}
