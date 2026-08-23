// atc-t3 is a disposable ATC-236 probe for driving an unmodified T3 Code
// server from Go. It intentionally exposes only discovery and orchestration,
// not T3's complete RPC contract.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/elevenideas/atc/experiments/t3-backend/internal/t3"
)

const (
	getConfigMethod       = "server.getConfig"
	dispatchCommandMethod = "orchestration.dispatchCommand"
	subscribeShellMethod  = "orchestration.subscribeShell"
	subscribeThreadMethod = "orchestration.subscribeThread"
)

type options struct {
	baseURL         string
	bootstrapToken  string
	provider        string
	model           string
	projectRoot     string
	scenario        string
	timeout         time.Duration
	reconnect       bool
	invalidProvider string
}

type serverConfig struct {
	Environment struct {
		EnvironmentID string `json:"environmentId"`
		ServerVersion string `json:"serverVersion"`
	} `json:"environment"`
	Providers []provider `json:"providers"`
}

type provider struct {
	InstanceID   string `json:"instanceId"`
	Driver       string `json:"driver"`
	DisplayName  string `json:"displayName"`
	Enabled      bool   `json:"enabled"`
	Installed    bool   `json:"installed"`
	Status       string `json:"status"`
	Availability string `json:"availability"`
	Auth         struct {
		Status string `json:"status"`
	} `json:"auth"`
	Models []struct {
		Slug      string `json:"slug"`
		Name      string `json:"name"`
		IsDefault bool   `json:"isDefault"`
	} `json:"models"`
}

type shellStreamItem struct {
	Kind     string `json:"kind"`
	Snapshot struct {
		SnapshotSequence int64 `json:"snapshotSequence"`
		Projects         []struct {
			ID            string `json:"id"`
			Title         string `json:"title"`
			WorkspaceRoot string `json:"workspaceRoot"`
		} `json:"projects"`
	} `json:"snapshot"`
}

type project struct {
	ID            string
	Title         string
	WorkspaceRoot string
}

type threadStreamItem struct {
	Kind     string          `json:"kind"`
	Snapshot *threadSnapshot `json:"snapshot,omitempty"`
	Event    *threadEvent    `json:"event,omitempty"`
}

type threadSnapshot struct {
	SnapshotSequence int64       `json:"snapshotSequence"`
	Thread           threadState `json:"thread"`
}

type threadState struct {
	ID         string          `json:"id"`
	Session    *sessionState   `json:"session"`
	LatestTurn *latestTurn     `json:"latestTurn"`
	Messages   []message       `json:"messages"`
	Activities []activity      `json:"activities"`
	Raw        json.RawMessage `json:"-"`
}

type sessionState struct {
	Status       string  `json:"status"`
	ActiveTurnID *string `json:"activeTurnId"`
	LastError    *string `json:"lastError"`
}

type latestTurn struct {
	TurnID string `json:"turnId"`
	State  string `json:"state"`
}

type message struct {
	ID        string  `json:"id"`
	Role      string  `json:"role"`
	Text      string  `json:"text"`
	Streaming bool    `json:"streaming"`
	TurnID    *string `json:"turnId"`
}

type activity struct {
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
}

type threadEvent struct {
	Sequence int64           `json:"sequence"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

type messagePayload struct {
	MessageID string  `json:"messageId"`
	Role      string  `json:"role"`
	Text      string  `json:"text"`
	Streaming bool    `json:"streaming"`
	TurnID    *string `json:"turnId"`
}

type sessionPayload struct {
	Session sessionState `json:"session"`
}

type activityPayload struct {
	Activity activity `json:"activity"`
}

type scenarioSpec struct {
	Name            string
	Prompt          string
	RuntimeMode     string
	InteractionMode string
	RequestAction   string
	InvalidProvider bool
}

type scenarioResult struct {
	Name                   string   `json:"name"`
	ThreadID               string   `json:"threadId"`
	TerminalState          string   `json:"terminalState"`
	AssistantOutput        string   `json:"assistantOutput,omitempty"`
	Lifecycle              []string `json:"lifecycle"`
	ObservedApproval       bool     `json:"observedApproval"`
	ObservedUserInput      bool     `json:"observedUserInput"`
	WaitingStates          []string `json:"waitingStates,omitempty"`
	Reconnected            bool     `json:"reconnected"`
	RecoveredPendingAction bool     `json:"recoveredPendingAction"`
	CancellationInferred   bool     `json:"cancellationInferred,omitempty"`
	Error                  string   `json:"error,omitempty"`
}

type probeReport struct {
	ObservedAt         string            `json:"observedAt"`
	ServerVersion      string            `json:"serverVersion"`
	EnvironmentID      string            `json:"environmentId"`
	Project            project           `json:"project"`
	SelectedProvider   string            `json:"selectedProvider"`
	SelectedModel      string            `json:"selectedModel"`
	DiscoveredProvider []providerSummary `json:"discoveredProviders"`
	Scenarios          []scenarioResult  `json:"scenarios"`
}

type providerSummary struct {
	InstanceID   string `json:"instanceId"`
	Driver       string `json:"driver"`
	Enabled      bool   `json:"enabled"`
	Installed    bool   `json:"installed"`
	Status       string `json:"status"`
	AuthStatus   string `json:"authStatus"`
	Availability string `json:"availability,omitempty"`
	ModelCount   int    `json:"modelCount"`
}

type runner struct {
	ctx       context.Context
	auth      t3.Auth
	client    *t3.Client
	project   project
	provider  string
	model     string
	reconnect bool
}

func main() {
	log.SetFlags(0)
	opts := parseOptions()
	if strings.TrimSpace(opts.bootstrapToken) == "" {
		log.Fatal("bootstrap token is required via --bootstrap-token or ATC_T3_BOOTSTRAP_TOKEN")
	}

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()
	auth, err := t3.ExchangeBootstrapCredential(
		ctx,
		&http.Client{Timeout: 15 * time.Second},
		opts.baseURL,
		opts.bootstrapToken,
	)
	if err != nil {
		log.Fatal(err)
	}
	client, err := connect(ctx, auth)
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	var config serverConfig
	if err := client.Call(ctx, getConfigMethod, map[string]any{}, &config); err != nil {
		log.Fatal(err)
	}
	selectedProvider, selectedModel, err := selectProvider(config.Providers, opts.provider, opts.model)
	if err != nil {
		log.Fatal(err)
	}
	selectedProject, err := readOrCreateProject(ctx, client, opts.projectRoot)
	if err != nil {
		log.Fatal(err)
	}

	report := probeReport{
		ObservedAt:         time.Now().UTC().Format(time.RFC3339Nano),
		ServerVersion:      config.Environment.ServerVersion,
		EnvironmentID:      config.Environment.EnvironmentID,
		Project:            selectedProject,
		SelectedProvider:   selectedProvider,
		SelectedModel:      selectedModel,
		DiscoveredProvider: summarizeProviders(config.Providers),
	}

	probe := &runner{
		ctx:       ctx,
		auth:      auth,
		client:    client,
		project:   selectedProject,
		provider:  selectedProvider,
		model:     selectedModel,
		reconnect: opts.reconnect,
	}
	for _, spec := range scenarios(opts.scenario) {
		if spec.InvalidProvider {
			probe.provider = opts.invalidProvider
			probe.model = "atc-236-missing-model"
		}
		result := probe.run(spec)
		report.Scenarios = append(report.Scenarios, result)
		probe.provider = selectedProvider
		probe.model = selectedModel
	}

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(string(encoded))
	for _, result := range report.Scenarios {
		if result.Error != "" {
			os.Exit(1)
		}
	}
}

func parseOptions() options {
	var opts options
	flag.StringVar(&opts.baseURL, "url", "http://127.0.0.1:17636", "T3 Code base URL")
	flag.StringVar(&opts.bootstrapToken, "bootstrap-token", os.Getenv("ATC_T3_BOOTSTRAP_TOKEN"), "one-time T3 pairing/bootstrap token")
	flag.StringVar(&opts.provider, "provider", "", "provider instance ID (auto-selects Codex or another ready provider)")
	flag.StringVar(&opts.model, "model", "", "provider model slug (auto-selects the default model)")
	flag.StringVar(&opts.projectRoot, "project-root", "", "workspace root from the T3 project registry")
	flag.StringVar(&opts.scenario, "scenario", "all", "basic, approval, input, cancel, failure, or all")
	flag.DurationVar(&opts.timeout, "timeout", 12*time.Minute, "whole-probe timeout")
	flag.BoolVar(&opts.reconnect, "reconnect-on-request", true, "reconnect while approval/input is pending")
	flag.StringVar(&opts.invalidProvider, "invalid-provider", "atc-236-missing-provider", "missing provider instance used by the failure scenario")
	flag.Parse()
	return opts
}

func scenarios(name string) []scenarioSpec {
	all := []scenarioSpec{
		{
			Name:            "basic",
			Prompt:          "Reply with exactly ATC_T3_BASIC_OK. Do not use tools.",
			RuntimeMode:     "full-access",
			InteractionMode: "default",
		},
		{
			Name:            "approval",
			Prompt:          "Use the shell to run exactly `printf ATC_T3_APPROVAL_OK`. Do not use any other tool. Then report its output.",
			RuntimeMode:     "approval-required",
			InteractionMode: "default",
			RequestAction:   "accept",
		},
		{
			Name:            "input",
			Prompt:          "Before answering, use request_user_input to ask me to choose Alpha or Beta, then repeat my choice.",
			RuntimeMode:     "full-access",
			InteractionMode: "plan",
			RequestAction:   "answer",
		},
		{
			Name:            "cancel",
			Prompt:          "Use the shell to run exactly `sleep 120`. Do not do anything else.",
			RuntimeMode:     "full-access",
			InteractionMode: "default",
			RequestAction:   "cancel",
		},
		{
			Name:            "failure",
			Prompt:          "Reply with ATC_T3_FAILURE_PROBE.",
			RuntimeMode:     "full-access",
			InteractionMode: "default",
			InvalidProvider: true,
		},
	}
	if name == "all" {
		return all
	}
	for _, scenario := range all {
		if scenario.Name == name {
			return []scenarioSpec{scenario}
		}
	}
	log.Fatalf("unknown scenario %q", name)
	return nil
}

func connect(ctx context.Context, auth t3.Auth) (*t3.Client, error) {
	socketURL, err := auth.WebSocketURL(ctx)
	if err != nil {
		return nil, err
	}
	return t3.Dial(ctx, socketURL)
}

func readOrCreateProject(ctx context.Context, client *t3.Client, wantedRoot string) (project, error) {
	selected, foundAny, err := readProject(ctx, client, wantedRoot)
	if err != nil {
		return project{}, err
	}
	if selected.ID != "" {
		return selected, nil
	}
	if wantedRoot == "" {
		return project{}, errors.New("T3 shell snapshot has no projects; pass --project-root to create the disposable probe project")
	}
	if foundAny {
		return project{}, fmt.Errorf("T3 shell snapshot has no project rooted at %q", wantedRoot)
	}
	command := map[string]any{
		"type": "project.create", "commandId": newID(), "projectId": newID(),
		"title": filepath.Base(wantedRoot), "workspaceRoot": wantedRoot, "createdAt": timestamp(),
	}
	var dispatchResult struct {
		Sequence int64 `json:"sequence"`
	}
	if err := client.Call(ctx, dispatchCommandMethod, command, &dispatchResult); err != nil {
		return project{}, fmt.Errorf("create probe project: %w", err)
	}
	selected, _, err = readProject(ctx, client, wantedRoot)
	if err != nil {
		return project{}, err
	}
	if selected.ID == "" {
		return project{}, errors.New("created probe project was absent from the next shell snapshot")
	}
	return selected, nil
}

func readProject(ctx context.Context, client *t3.Client, wantedRoot string) (project, bool, error) {
	subscription, err := client.Subscribe(ctx, subscribeShellMethod, map[string]any{
		"requestCompletionMarker": true,
	})
	if err != nil {
		return project{}, false, err
	}
	defer subscription.Close()
	for {
		select {
		case <-ctx.Done():
			return project{}, false, ctx.Err()
		case err := <-subscription.Done:
			if err == nil {
				err = errors.New("shell subscription ended before a snapshot")
			}
			return project{}, false, err
		case raw, ok := <-subscription.Items:
			if !ok {
				continue
			}
			var item shellStreamItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return project{}, false, fmt.Errorf("decode shell stream item: %w", err)
			}
			if item.Kind != "snapshot" {
				continue
			}
			for _, candidate := range item.Snapshot.Projects {
				if wantedRoot == "" || candidate.WorkspaceRoot == wantedRoot {
					return project{ID: candidate.ID, Title: candidate.Title, WorkspaceRoot: candidate.WorkspaceRoot}, true, nil
				}
			}
			return project{}, len(item.Snapshot.Projects) > 0, nil
		}
	}
}

func selectProvider(providers []provider, wantedProvider string, wantedModel string) (string, string, error) {
	candidates := append([]provider(nil), providers...)
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Driver == "codex" && candidates[j].Driver != "codex"
	})
	for _, candidate := range candidates {
		if wantedProvider != "" && candidate.InstanceID != wantedProvider {
			continue
		}
		if !candidate.Enabled || !candidate.Installed || candidate.Status == "error" || candidate.Availability == "unavailable" {
			continue
		}
		if candidate.Auth.Status == "unauthenticated" || len(candidate.Models) == 0 {
			continue
		}
		for _, model := range candidate.Models {
			if wantedModel == model.Slug || (wantedModel == "" && model.IsDefault) {
				return candidate.InstanceID, model.Slug, nil
			}
		}
		if wantedModel == "" {
			return candidate.InstanceID, candidate.Models[0].Slug, nil
		}
	}
	return "", "", fmt.Errorf("no ready provider/model matched provider=%q model=%q", wantedProvider, wantedModel)
}

func summarizeProviders(providers []provider) []providerSummary {
	summaries := make([]providerSummary, 0, len(providers))
	for _, candidate := range providers {
		summaries = append(summaries, providerSummary{
			InstanceID: candidate.InstanceID, Driver: candidate.Driver, Enabled: candidate.Enabled,
			Installed: candidate.Installed, Status: candidate.Status, AuthStatus: candidate.Auth.Status,
			Availability: candidate.Availability, ModelCount: len(candidate.Models),
		})
	}
	return summaries
}

func (r *runner) run(spec scenarioSpec) scenarioResult {
	result := scenarioResult{Name: spec.Name}
	threadID := newID()
	result.ThreadID = threadID
	now := timestamp()
	selection := map[string]any{"instanceId": r.provider, "model": r.model}
	create := map[string]any{
		"type": "thread.create", "commandId": newID(), "threadId": threadID,
		"projectId": r.project.ID, "title": "ATC-236 " + spec.Name,
		"modelSelection": selection, "runtimeMode": spec.RuntimeMode,
		"interactionMode": spec.InteractionMode, "branch": nil, "worktreePath": nil,
		"createdAt": now,
	}
	if err := r.dispatch(create); err != nil {
		result.Error = fmt.Sprintf("create thread: %v", err)
		result.TerminalState = "dispatch-error"
		return result
	}

	subscription, err := r.subscribeThread(threadID)
	if err != nil {
		result.Error = fmt.Sprintf("subscribe thread: %v", err)
		return result
	}
	defer func() { subscription.Close() }()
	if _, err := awaitSnapshot(r.ctx, subscription); err != nil {
		result.Error = fmt.Sprintf("initial thread snapshot: %v", err)
		return result
	}

	start := map[string]any{
		"type": "thread.turn.start", "commandId": newID(), "threadId": threadID,
		"message": map[string]any{
			"messageId": newID(), "role": "user", "text": spec.Prompt, "attachments": []any{},
		},
		"modelSelection": selection, "runtimeMode": spec.RuntimeMode,
		"interactionMode": spec.InteractionMode, "createdAt": timestamp(),
	}
	if err := r.dispatch(start); err != nil {
		result.TerminalState = "dispatch-error"
		result.Error = fmt.Sprintf("start turn: %v", err)
		if spec.InvalidProvider {
			result.TerminalState = "failed"
			result.Error = ""
		}
		return result
	}

	state := lifecycleState{assistant: make(map[string]string)}
	for {
		select {
		case <-r.ctx.Done():
			result.Error = r.ctx.Err().Error()
			return result
		case streamErr := <-subscription.Done:
			if streamErr == nil {
				streamErr = errors.New("thread subscription ended unexpectedly")
			}
			result.Error = streamErr.Error()
			return result
		case raw, ok := <-subscription.Items:
			if !ok {
				continue
			}
			var item threadStreamItem
			if err := json.Unmarshal(raw, &item); err != nil {
				result.Error = fmt.Sprintf("decode thread stream item: %v", err)
				return result
			}
			request := state.apply(item)
			if request != nil {
				result.ObservedApproval = result.ObservedApproval || request.kind == "approval"
				result.ObservedUserInput = result.ObservedUserInput || request.kind == "user-input"
				result.WaitingStates = append(result.WaitingStates, request.kind)
				if r.reconnect && spec.RequestAction != "cancel" {
					subscription.Close()
					_ = r.client.Close()
					r.client, err = connect(r.ctx, r.auth)
					if err != nil {
						result.Error = fmt.Sprintf("reconnect: %v", err)
						return result
					}
					subscription, err = r.subscribeThread(threadID)
					if err != nil {
						result.Error = fmt.Sprintf("resubscribe: %v", err)
						return result
					}
					recovered, snapshotErr := awaitSnapshot(r.ctx, subscription)
					if snapshotErr != nil {
						result.Error = fmt.Sprintf("recovery snapshot: %v", snapshotErr)
						return result
					}
					result.Reconnected = true
					result.RecoveredPendingAction = snapshotHasRequest(recovered.Thread, request.id, request.kind)
					state.applySnapshot(recovered.Thread)
				}
				if err := r.handleRequest(threadID, spec.RequestAction, *request); err != nil {
					result.Error = fmt.Sprintf("handle %s request: %v", request.kind, err)
					return result
				}
			}
			if spec.RequestAction == "cancel" && state.sawToolStart && !state.cancelRequested {
				command := map[string]any{
					"type": "thread.turn.interrupt", "commandId": newID(), "threadId": threadID,
					"createdAt": timestamp(),
				}
				if state.session != nil && state.session.ActiveTurnID != nil {
					command["turnId"] = *state.session.ActiveTurnID
				}
				if err := r.dispatch(command); err != nil {
					result.Error = fmt.Sprintf("interrupt running tool: %v", err)
					return result
				}
				state.cancelRequested = true
			}
			if terminal := state.terminal(); terminal != "" {
				result.TerminalState = terminal
				result.CancellationInferred = terminal == "canceled" && state.cancelRequested
				result.AssistantOutput = state.output()
				result.Lifecycle = append([]string(nil), state.lifecycle...)
				if spec.InvalidProvider && terminal != "failed" {
					result.Error = "missing provider scenario did not reach failed lifecycle"
				}
				return result
			}
		}
	}
}

func (r *runner) subscribeThread(threadID string) (*t3.Subscription, error) {
	return r.client.Subscribe(r.ctx, subscribeThreadMethod, map[string]any{
		"threadId": threadID, "requestCompletionMarker": true,
	})
}

func (r *runner) dispatch(command map[string]any) error {
	var result struct {
		Sequence int64 `json:"sequence"`
	}
	return r.client.Call(r.ctx, dispatchCommandMethod, command, &result)
}

type pendingRequest struct {
	id        string
	kind      string
	questions []question
}

type question struct {
	ID          string `json:"id"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label string `json:"label"`
	} `json:"options"`
}

func (r *runner) handleRequest(threadID string, action string, request pendingRequest) error {
	switch action {
	case "accept":
		return r.dispatch(map[string]any{
			"type": "thread.approval.respond", "commandId": newID(), "threadId": threadID,
			"requestId": request.id, "decision": "accept", "createdAt": timestamp(),
		})
	case "answer":
		answers := make(map[string]any)
		for _, question := range request.questions {
			if len(question.Options) == 0 {
				return fmt.Errorf("question %q has no options", question.ID)
			}
			if question.MultiSelect {
				answers[question.ID] = []string{question.Options[0].Label}
			} else {
				answers[question.ID] = question.Options[0].Label
			}
		}
		return r.dispatch(map[string]any{
			"type": "thread.user-input.respond", "commandId": newID(), "threadId": threadID,
			"requestId": request.id, "answers": answers, "createdAt": timestamp(),
		})
	case "":
		return nil
	default:
		return fmt.Errorf("unknown request action %q", action)
	}
}

type lifecycleState struct {
	session         *sessionState
	latestTurn      *latestTurn
	assistant       map[string]string
	order           []string
	lifecycle       []string
	seenRequest     map[string]bool
	sawToolStart    bool
	cancelRequested bool
}

func (s *lifecycleState) apply(item threadStreamItem) *pendingRequest {
	if s.seenRequest == nil {
		s.seenRequest = make(map[string]bool)
	}
	if item.Kind == "snapshot" && item.Snapshot != nil {
		s.applySnapshot(item.Snapshot.Thread)
		return firstPendingRequest(item.Snapshot.Thread.Activities, s.seenRequest)
	}
	if item.Kind != "event" || item.Event == nil {
		return nil
	}
	switch item.Event.Type {
	case "thread.session-set":
		var payload sessionPayload
		if json.Unmarshal(item.Event.Payload, &payload) == nil {
			s.setSession(payload.Session)
		}
	case "thread.message-sent":
		var payload messagePayload
		if json.Unmarshal(item.Event.Payload, &payload) == nil && payload.Role == "assistant" {
			if _, exists := s.assistant[payload.MessageID]; !exists {
				s.order = append(s.order, payload.MessageID)
			}
			if payload.Streaming {
				s.assistant[payload.MessageID] += payload.Text
			} else if payload.Text != "" {
				s.assistant[payload.MessageID] = payload.Text
			}
		}
	case "thread.activity-appended":
		var payload activityPayload
		if json.Unmarshal(item.Event.Payload, &payload) == nil {
			s.sawToolStart = s.sawToolStart || payload.Activity.Kind == "tool.started"
			return pendingFromActivity(payload.Activity, s.seenRequest)
		}
	}
	return nil
}

func (s *lifecycleState) applySnapshot(thread threadState) {
	if thread.Session != nil {
		s.setSession(*thread.Session)
	}
	s.latestTurn = thread.LatestTurn
	for _, message := range thread.Messages {
		if message.Role != "assistant" {
			continue
		}
		if _, exists := s.assistant[message.ID]; !exists {
			s.order = append(s.order, message.ID)
		}
		s.assistant[message.ID] = message.Text
	}
}

func (s *lifecycleState) setSession(session sessionState) {
	if s.session == nil || s.session.Status != session.Status {
		s.lifecycle = append(s.lifecycle, session.Status)
	}
	s.session = &session
	if s.latestTurn == nil || s.latestTurn.State != "running" {
		return
	}
	switch session.Status {
	case "idle", "ready":
		s.latestTurn.State = "completed"
	case "error":
		s.latestTurn.State = "error"
	case "interrupted", "stopped":
		s.latestTurn.State = "interrupted"
	}
}

func (s *lifecycleState) terminal() string {
	if s.session == nil {
		return ""
	}
	switch s.session.Status {
	case "error":
		return "failed"
	case "interrupted", "stopped":
		return "canceled"
	case "idle", "ready":
		if s.latestTurn != nil && s.latestTurn.State == "running" {
			return ""
		}
		if s.cancelRequested {
			return "canceled"
		}
		return "completed"
	default:
		return ""
	}
}

func (s *lifecycleState) output() string {
	var output strings.Builder
	for _, id := range s.order {
		output.WriteString(s.assistant[id])
	}
	return output.String()
}

func firstPendingRequest(activities []activity, seen map[string]bool) *pendingRequest {
	resolved := make(map[string]bool)
	for _, candidate := range activities {
		id, _ := candidate.Payload["requestId"].(string)
		if id == "" {
			continue
		}
		if candidate.Kind == "approval.resolved" || candidate.Kind == "user-input.resolved" {
			resolved[id] = true
		}
	}
	for _, candidate := range activities {
		request := pendingFromActivity(candidate, seen)
		if request != nil && !resolved[request.id] {
			return request
		}
	}
	return nil
}

func pendingFromActivity(candidate activity, seen map[string]bool) *pendingRequest {
	if candidate.Kind != "approval.requested" && candidate.Kind != "user-input.requested" {
		return nil
	}
	id, _ := candidate.Payload["requestId"].(string)
	if id == "" || seen[id] {
		return nil
	}
	seen[id] = true
	request := &pendingRequest{id: id, kind: "approval"}
	if candidate.Kind == "user-input.requested" {
		request.kind = "user-input"
		encoded, _ := json.Marshal(candidate.Payload["questions"])
		_ = json.Unmarshal(encoded, &request.questions)
	}
	return request
}

func snapshotHasRequest(thread threadState, requestID string, kind string) bool {
	activityKind := kind + ".requested"
	for _, candidate := range thread.Activities {
		id, _ := candidate.Payload["requestId"].(string)
		if candidate.Kind == activityKind && id == requestID {
			return true
		}
	}
	return false
}

func awaitSnapshot(ctx context.Context, subscription *t3.Subscription) (threadSnapshot, error) {
	for {
		select {
		case <-ctx.Done():
			return threadSnapshot{}, ctx.Err()
		case err := <-subscription.Done:
			if err == nil {
				err = errors.New("thread subscription ended before snapshot")
			}
			return threadSnapshot{}, err
		case raw, ok := <-subscription.Items:
			if !ok {
				continue
			}
			var item threadStreamItem
			if err := json.Unmarshal(raw, &item); err != nil {
				return threadSnapshot{}, err
			}
			if item.Kind == "snapshot" && item.Snapshot != nil {
				return *item.Snapshot, nil
			}
		}
	}
}

func newID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	bytes[6] = (bytes[6] & 0x0f) | 0x40
	bytes[8] = (bytes[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(bytes[:])
	return encoded[0:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:32]
}

func timestamp() string {
	return time.Now().UTC().Format(time.RFC3339Nano)
}
