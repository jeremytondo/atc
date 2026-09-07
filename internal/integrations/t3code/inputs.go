package t3code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/threads"
)

// Structured requests (ATC-308). T3 reports an agent's questions as one
// user-input.requested activity carrying a request id and a set of
// questions, each with options and two allowances — a custom text
// answer, allowed unless the question says otherwise (T3's own surfaces
// read it the same way), and several selections when the question says
// so. An answer is one thread.user-input.respond command with one
// answer map against the request id: the option's value (its label
// when it names none) or the custom text, as a string for a
// single-selection question and a list for a multi-selection one,
// exactly as T3's surfaces submit. Evidence comes from the same
// activities: user-input.resolved names the answers the provider took
// (an abandoned question resolves with none), and
// provider.user-input.respond.failed carries T3's detail — stale wording
// resolves the request, as in T3's own projection. Content ATC cannot
// answer faithfully is reported with the reason rather than trimmed.

// inputPayload is the payload of the user-input activities: the request
// id every one carries, the questions of a request (read in a second
// stage, so a question shape ATC cannot read makes the request
// unanswerable rather than invisible), the answers of a resolution, the
// detail of a failure.
type inputPayload struct {
	RequestID string          `json:"requestId"`
	Questions json.RawMessage `json:"questions"`
	Answers   json.RawMessage `json:"answers"`
	Detail    string          `json:"detail"`
}

type inputQuestion struct {
	ID       string `json:"id"`
	Header   string `json:"header"`
	Question string `json:"question"`
	Options  []struct {
		Label       string  `json:"label"`
		Description string  `json:"description"`
		Value       *string `json:"value"`
	} `json:"options"`
	AllowCustomAnswer *bool `json:"allowCustomAnswer"`
	MultiSelect       bool  `json:"multiSelect"`
}

// PrepareAnswer resolves an answer against the live connection: the
// Integration must be connected and T3 must still report the thread.
// Nothing is sent until the dispatch runs.
func (s *Service) PrepareAnswer(ctx context.Context, providerID string) (integrations.AnswerDispatch, error) {
	client, err := s.prepareCommand(providerID)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, answer integrations.InputAnswer) error {
		err := outcome(s.deliver(ctx, client, answerCommand(providerID, answer)), integrations.ErrAnswerRejected, "answer")
		if err == nil || errors.Is(err, integrations.ErrDeliveryUncertain) {
			// Committed or uncertain, T3 may resolve the request now:
			// watch for the evidence.
			s.watch(providerID)
		}
		return err
	}, nil
}

// prepareCommand is the check every command to an existing thread
// makes: the Integration connected, the thread still reported. It
// returns the connection the command goes to — the one the thread was
// resolved on.
func (s *Service) prepareCommand(providerID string) (*rpcClient, error) {
	s.mu.Lock()
	connection, client := s.connection, s.client
	_, known := s.shell.threads[providerID]
	s.mu.Unlock()
	if connection.State != api.IntegrationConnected {
		return nil, fmt.Errorf("%w: T3 Code is %s: %s", integrations.ErrNotConnected, connection.State, connection.Detail)
	}
	if !known {
		return nil, fmt.Errorf("%w: T3 Code no longer reports the thread; it may be archived or deleted there", integrations.ErrNotConnected)
	}
	return client, nil
}

// answerCommand is the thread.user-input.respond command for an answer,
// its identity derived from the answer's.
func answerCommand(threadID string, answer integrations.InputAnswer) map[string]any {
	answers := make(map[string]any, len(answer.Answers))
	for _, a := range answer.Answers {
		switch {
		case a.Multiple:
			values := a.Values
			if values == nil {
				values = []string{}
			}
			answers[a.QuestionID] = values
		case len(a.Values) > 0:
			answers[a.QuestionID] = a.Values[0]
		default:
			answers[a.QuestionID] = ""
		}
	}
	return map[string]any{
		"type":      "thread.user-input.respond",
		"commandId": ids.UUIDFrom("t3code/answer/" + answer.Key),
		"threadId":  threadID,
		"requestId": answer.RequestID,
		"answers":   answers,
		"createdAt": timestamp(answer.CreatedAt),
	}
}

// pendingInputs derives the pending requests from a thread's
// activities, in request order — each user-input.requested without a
// later user-input.resolved for its request id, or a respond failure
// naming the request stale — and every resolution and failure as
// evidence.
func pendingInputs(activities []activity) ([]threads.InputObservation, []threads.InputResolution) {
	var pending []threads.InputObservation
	var resolutions []threads.InputResolution
	resolved := map[string]bool{}
	seen := map[string]bool{}
	for _, a := range activities {
		var payload inputPayload
		if json.Unmarshal(a.Payload, &payload) != nil || payload.RequestID == "" {
			continue
		}
		switch a.Kind {
		case "user-input.requested":
			if seen[payload.RequestID] {
				continue
			}
			seen[payload.RequestID] = true
			pending = append(pending, inputObservation(payload, a))
		case "user-input.resolved":
			resolved[payload.RequestID] = true
			resolutions = append(resolutions, threads.InputResolution{RequestID: payload.RequestID, Answers: providerAnswers(payload.Answers), At: a.CreatedAt})
		case "provider.user-input.respond.failed":
			if stale(payload.Detail) {
				resolved[payload.RequestID] = true
			}
			resolutions = append(resolutions, threads.InputResolution{RequestID: payload.RequestID, Failure: payload.Detail, Stale: stale(payload.Detail), At: a.CreatedAt})
		}
	}
	kept := pending[:0]
	for _, request := range pending {
		if !resolved[request.RequestID] {
			kept = append(kept, request)
		}
	}
	return kept, resolutions
}

// inputObservation normalizes one request: its questions with ATC's
// allowances, or the reason it cannot be answered through ATC.
func inputObservation(payload inputPayload, a activity) threads.InputObservation {
	o := threads.InputObservation{RequestID: payload.RequestID, RequestedAt: a.CreatedAt}
	var questions []inputQuestion
	if len(payload.Questions) > 0 && json.Unmarshal(payload.Questions, &questions) != nil {
		o.Unanswerable = "the request's questions are in a shape ATC cannot read"
		return o
	}
	if len(questions) == 0 {
		o.Unanswerable = "the request carries no questions"
	}
	ids := map[string]bool{}
	for i, q := range questions {
		question := threads.QuestionObservation{
			ProviderID: q.ID, Header: q.Header, Text: q.Question, Options: []api.InputOption{},
			AllowsCustom: q.AllowCustomAnswer == nil || *q.AllowCustomAnswer, AllowsMultiple: q.MultiSelect,
		}
		for _, option := range q.Options {
			value := option.Label
			if option.Value != nil {
				value = *option.Value
			}
			question.Options = append(question.Options, api.InputOption{Value: value, Label: option.Label, Description: option.Description})
		}
		o.Questions = append(o.Questions, question)
		var problem string
		switch {
		case strings.TrimSpace(q.ID) == "" || strings.TrimSpace(q.Question) == "":
			problem = "has no id or text"
		case ids[q.ID]:
			problem = "repeats an earlier question's id"
		case len(question.Options) == 0 && !question.AllowsCustom:
			problem = "offers no choices and allows no custom text"
		case hasBlankOption(question.Options):
			problem = "offers a choice with no value"
		}
		ids[q.ID] = true
		if problem != "" && o.Unanswerable == "" {
			o.Unanswerable = fmt.Sprintf("question %d %s", i+1, problem)
		}
	}
	return o
}

func hasBlankOption(options []api.InputOption) bool {
	for _, option := range options {
		if option.Value == "" {
			return true
		}
	}
	return false
}

// providerAnswers reads a resolution's answer map: a string is one
// value, a list of strings several; anything else is dropped.
func providerAnswers(raw json.RawMessage) []threads.ProviderAnswer {
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	answers := make([]threads.ProviderAnswer, 0, len(m))
	for id, value := range m {
		var one string
		if json.Unmarshal(value, &one) == nil {
			answers = append(answers, threads.ProviderAnswer{QuestionID: id, Values: []string{one}})
			continue
		}
		var many []string
		if json.Unmarshal(value, &many) == nil {
			answers = append(answers, threads.ProviderAnswer{QuestionID: id, Values: many, Multiple: true})
		}
	}
	return answers
}
