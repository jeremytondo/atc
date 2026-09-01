// Command probe is the smallest read-only proof for ATC-285. It fetches the
// T3 Code shell snapshot, projects T3 lifecycle evidence into ATC statuses,
// and can report threads that appear or change while it watches.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/api"
)

const usage = `Usage:
  ./scripts/atc-285 snapshot [--project-root PATH]
  ./scripts/atc-285 watch [--project-root PATH] [--interval DURATION]

Options:
  --url URL            T3 Code environment origin (or ATC_T3_ORIGIN).
  --project-root PATH  Only show threads in this exact T3 workspace root.
  --interval DURATION  Watch polling interval (default 1s).

The wrapper obtains a temporary orchestration:read bearer token when
ATC_T3_BEARER_TOKEN is unset and revokes it when the probe exits.`

type client struct {
	origin string
	token  string
	http   *http.Client
}

type shellSnapshot struct {
	Sequence  *uint64         `json:"snapshotSequence"`
	Projects  *[]projectShell `json:"projects"`
	Threads   *[]threadShell  `json:"threads"`
	UpdatedAt time.Time       `json:"updatedAt"`
}

type projectShell struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
	WorkspaceRoot string `json:"workspaceRoot"`
}

type threadShell struct {
	ID                  string        `json:"id"`
	ProjectID           string        `json:"projectId"`
	Title               string        `json:"title"`
	Session             *sessionShell `json:"session"`
	HasPendingApprovals *bool         `json:"hasPendingApprovals"`
	HasPendingUserInput *bool         `json:"hasPendingUserInput"`
	BackgroundLiveness  *string       `json:"backgroundLiveness"`
	UpdatedAt           time.Time     `json:"updatedAt"`
}

type sessionShell struct {
	Status    string  `json:"status"`
	LastError *string `json:"lastError"`
}

type projectedThread struct {
	ID            string           `json:"id"`
	ProjectID     string           `json:"projectId"`
	Project       string           `json:"project"`
	WorkspaceRoot string           `json:"workspaceRoot"`
	Title         string           `json:"title"`
	Status        api.ThreadStatus `json:"status"`
	NativeStatus  string           `json:"nativeStatus,omitempty"`
	LastError     string           `json:"lastError,omitempty"`
	UpdatedAt     time.Time        `json:"updatedAt"`
}

type change struct {
	Kind   string          `json:"kind"`
	Thread projectedThread `json:"thread"`
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "atc-285:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := fmt.Fprintln(output, usage)
		return err
	}
	command := args[0]
	if command != "snapshot" && command != "watch" {
		return fmt.Errorf("unknown command %q\n\n%s", command, usage)
	}

	flags := flag.NewFlagSet("atc-285", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	origin := flags.String("url", os.Getenv("ATC_T3_ORIGIN"), "T3 Code origin")
	projectRoot := flags.String("project-root", "", "exact T3 workspace root")
	interval := flags.Duration("interval", time.Second, "watch polling interval")
	if err := flags.Parse(args[1:]); err != nil {
		return fmt.Errorf("parse arguments: %w", err)
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %s", strings.Join(flags.Args(), " "))
	}
	if *origin == "" {
		return errors.New("T3 Code origin is required (--url or ATC_T3_ORIGIN)")
	}
	token := os.Getenv("ATC_T3_BEARER_TOKEN")
	if token == "" {
		return errors.New("T3 Code bearer token is required (ATC_T3_BEARER_TOKEN)")
	}
	c, err := newClient(*origin, token, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		return err
	}

	switch command {
	case "snapshot":
		threads, err := c.fetch(ctx, *projectRoot)
		if err != nil {
			return err
		}
		return writeChanges(output, presentChanges(threads))
	case "watch":
		if *interval <= 0 {
			return errors.New("interval must be positive")
		}
		watchCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
		defer stop()
		return watch(watchCtx, c, *projectRoot, *interval, output)
	}
	return nil
}

func newClient(origin, token string, httpClient *http.Client) (*client, error) {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, fmt.Errorf("invalid T3 Code origin %q", origin)
	}
	if (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("invalid T3 Code origin %q: path, query, and fragment are not allowed", origin)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &client{origin: strings.TrimRight(origin, "/"), token: token, http: httpClient}, nil
}

func (c *client) fetch(ctx context.Context, projectRoot string) ([]projectedThread, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.origin+"/api/orchestration/shell", nil)
	if err != nil {
		return nil, fmt.Errorf("build T3 shell request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	response, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("T3 Code unavailable at %s: %w", c.origin, err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		switch response.StatusCode {
		case http.StatusUnauthorized:
			return nil, errors.New("T3 Code rejected the credential: token is missing, expired, or invalid")
		case http.StatusForbidden:
			return nil, errors.New("T3 Code rejected the credential: orchestration:read scope is required")
		default:
			return nil, fmt.Errorf("T3 Code shell returned %s", response.Status)
		}
	}

	var snapshot shellSnapshot
	if err := json.NewDecoder(io.LimitReader(response.Body, 16<<20)).Decode(&snapshot); err != nil {
		return nil, fmt.Errorf("decode T3 Code shell schema: %w", err)
	}
	if err := validateSnapshot(snapshot); err != nil {
		return nil, fmt.Errorf("decode T3 Code shell schema: %w", err)
	}
	return project(snapshot, projectRoot), nil
}

func validateSnapshot(snapshot shellSnapshot) error {
	if snapshot.Sequence == nil || snapshot.Projects == nil || snapshot.Threads == nil || snapshot.UpdatedAt.IsZero() {
		return errors.New("snapshotSequence, projects, threads, or updatedAt is missing")
	}
	projects := make(map[string]struct{}, len(*snapshot.Projects))
	for _, project := range *snapshot.Projects {
		if project.ID == "" || project.Title == "" || project.WorkspaceRoot == "" {
			return errors.New("project id, title, or workspaceRoot is missing")
		}
		if _, exists := projects[project.ID]; exists {
			return fmt.Errorf("duplicate project %s", project.ID)
		}
		projects[project.ID] = struct{}{}
	}
	threads := make(map[string]struct{}, len(*snapshot.Threads))
	for _, thread := range *snapshot.Threads {
		if thread.ID == "" || thread.ProjectID == "" || thread.Title == "" || thread.UpdatedAt.IsZero() {
			return errors.New("thread id, projectId, title, or updatedAt is missing")
		}
		if _, ok := projects[thread.ProjectID]; !ok {
			return fmt.Errorf("thread %s names unknown project %s", thread.ID, thread.ProjectID)
		}
		if thread.HasPendingApprovals == nil || thread.HasPendingUserInput == nil {
			return fmt.Errorf("thread %s is missing pending-action flags", thread.ID)
		}
		if thread.Session != nil && thread.Session.Status == "" {
			return fmt.Errorf("thread %s session status is missing", thread.ID)
		}
		if _, exists := threads[thread.ID]; exists {
			return fmt.Errorf("duplicate thread %s", thread.ID)
		}
		threads[thread.ID] = struct{}{}
	}
	return nil
}

func project(snapshot shellSnapshot, projectRoot string) []projectedThread {
	projects := make(map[string]projectShell, len(*snapshot.Projects))
	for _, project := range *snapshot.Projects {
		projects[project.ID] = project
	}
	result := make([]projectedThread, 0, len(*snapshot.Threads))
	for _, thread := range *snapshot.Threads {
		owner := projects[thread.ProjectID]
		if projectRoot != "" && owner.WorkspaceRoot != projectRoot {
			continue
		}
		status, native, lastError := projectStatus(thread)
		result = append(result, projectedThread{
			ID:            thread.ID,
			ProjectID:     thread.ProjectID,
			Project:       owner.Title,
			WorkspaceRoot: owner.WorkspaceRoot,
			Title:         thread.Title,
			Status:        status,
			NativeStatus:  native,
			LastError:     lastError,
			UpdatedAt:     thread.UpdatedAt,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].UpdatedAt.Equal(result[j].UpdatedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].UpdatedAt.After(result[j].UpdatedAt)
	})
	return result
}

func projectStatus(thread threadShell) (api.ThreadStatus, string, string) {
	native := "none"
	lastError := ""
	if thread.Session != nil {
		native = thread.Session.Status
		if thread.Session.LastError != nil {
			lastError = *thread.Session.LastError
		}
	}
	if *thread.HasPendingApprovals {
		return api.ThreadWaitingForPermission, native, lastError
	}
	if *thread.HasPendingUserInput {
		return api.ThreadWaitingForInput, native, lastError
	}
	if thread.Session != nil {
		switch thread.Session.Status {
		case "starting", "running":
			return api.ThreadWorking, native, lastError
		case "error":
			return api.ThreadError, native, lastError
		case "idle", "ready", "interrupted", "stopped":
			// A known background state below can still make this working.
		default:
			return api.ThreadUnknown, native, lastError
		}
	}
	if thread.BackgroundLiveness != nil {
		switch *thread.BackgroundLiveness {
		case "working", "monitoring":
			return api.ThreadWorking, native, lastError
		case "":
		default:
			return api.ThreadUnknown, native, lastError
		}
	}
	return api.ThreadIdle, native, lastError
}

func watch(ctx context.Context, c *client, projectRoot string, interval time.Duration, output io.Writer) error {
	current, err := c.fetch(ctx, projectRoot)
	if err != nil {
		return err
	}
	if err := writeChanges(output, presentChanges(current)); err != nil {
		return err
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			next, err := c.fetch(ctx, projectRoot)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			if err := writeChanges(output, diff(current, next)); err != nil {
				return err
			}
			current = next
		}
	}
}

func presentChanges(threads []projectedThread) []change {
	changes := make([]change, 0, len(threads))
	for _, thread := range threads {
		changes = append(changes, change{Kind: "present", Thread: thread})
	}
	return changes
}

func diff(before, after []projectedThread) []change {
	previous := make(map[string]projectedThread, len(before))
	for _, thread := range before {
		previous[thread.ID] = thread
	}
	var changes []change
	for _, thread := range after {
		old, ok := previous[thread.ID]
		switch {
		case !ok:
			changes = append(changes, change{Kind: "created", Thread: thread})
		case old.Status != thread.Status:
			changes = append(changes, change{Kind: "status_changed", Thread: thread})
		}
		delete(previous, thread.ID)
	}
	for _, thread := range previous {
		changes = append(changes, change{Kind: "removed", Thread: thread})
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Thread.ID < changes[j].Thread.ID })
	return changes
}

func writeChanges(output io.Writer, changes []change) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	for _, item := range changes {
		if err := encoder.Encode(item); err != nil {
			return fmt.Errorf("write projection: %w", err)
		}
	}
	return nil
}
