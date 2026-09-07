package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

// Input requests and answers (ATC-308, ATC-309): the structured
// questions an agent is blocked on, as the producing Integration
// observes them, and the answer ATC submits against a request — a
// structured set covering the questions the client answered, each in a
// form it allows, or a conversational reply passed to the agent
// verbatim to interpret against its questions. A set need not be
// complete: the provider forwards whatever is answered and the agent
// reads what it received, so completeness is a surface's own rule, not
// ATC's (the T3 Code evidence is in internal/integrations/t3code). Requests
// are evidence held like approvals — a report replaces the pending set,
// ids derive from the private request identity, a resolved request is
// remembered for a while — with positional question ids (q1, q2, …) so
// no provider question id reaches the wire. An answer is durable: it is
// recorded under the request before anything is dispatched, so a retry
// recovers the same submission, and it resolves only on the provider's
// evidence — the request resolved with exactly these answers is the
// answer resolved; resolved otherwise, the answer is superseded; a
// failure the provider reports after the submission is the answer
// failed. The provider committing the command is delivery, never
// resolution.

const (
	inputPrefix  = "inpt-"
	answerPrefix = "ans-"
	// answersKept bounds the answers retained per thread; an unresolved
	// answer is never pruned.
	answersKept = 16
)

// ErrInputNotFound reports an input request id the thread has no record
// of; the API layer maps it to 404.
var ErrInputNotFound = errors.New("input request not found")

// ErrInputResolved refuses answering a request that is already resolved
// — through ATC, elsewhere, or by a stop; the API layer maps it to 409.
var ErrInputResolved = errors.New("input request already resolved")

// ErrInputUnanswerable refuses answering a request whose content ATC
// cannot faithfully answer; the reason is the request's own. The API
// layer maps it to 400.
var ErrInputUnanswerable = errors.New("input request cannot be answered through ATC")

// ErrAnswerInvalid refuses an answer that does not answer the request:
// nothing answered, a question unknown or answered twice, a choice not
// offered, a form or count the question does not allow, or a reply and
// structured answers together. The API layer maps it to 400.
var ErrAnswerInvalid = errors.New("invalid answer")

// ErrAnswerPending refuses a different answer while one already sent
// awaits the provider's evidence: uncertainty is never turned into a
// second answer. The API layer maps it to 409.
var ErrAnswerPending = errors.New("a different answer awaits the provider's evidence")

// InputObservation is one pending structured request as an Integration
// reports it: the provider's own request id (private), its questions in
// the provider's order, and — when the content is something ATC cannot
// answer faithfully — why, so the request is shown honestly rather than
// trimmed to look answerable.
type InputObservation struct {
	RequestID    string
	Questions    []QuestionObservation
	Unanswerable string
	RequestedAt  time.Time
}

// QuestionObservation is one question as the provider presents it, with
// the provider's own question id (private; the wire id is positional).
type QuestionObservation struct {
	ProviderID     string
	Header         string
	Text           string
	Options        []api.InputOption
	AllowsCustom   bool
	AllowsMultiple bool
}

// ProviderAnswer is one question's answer in the provider's terms: its
// question id, the values chosen (or the custom text as the one value),
// and whether the question takes several — the provider's wire shape
// differs.
type ProviderAnswer struct {
	QuestionID string   `json:"questionId"`
	Values     []string `json:"values"`
	Multiple   bool     `json:"multiple,omitempty"`
}

// InputResolution is the provider's evidence about one request's
// outcome, as the Integration reads it: the request resolved with an
// answer set (Answers; empty for a request the provider abandoned), or
// a response that failed with the provider's detail — Stale when the
// provider says the request was no longer live, which resolves the
// request. At is the provider's time of the evidence: a resolution
// counts for an answer when it is not older than the answer, a failure
// only when it carries the answer's own instant (the provider stamps a
// failure with the command that caused it).
type InputResolution struct {
	RequestID string
	Answers   []ProviderAnswer
	Failure   string
	Stale     bool
	At        time.Time
}

// AnswerRequest is what an Integration needs to dispatch an answer: the
// provider's request id, the answers in the provider's terms, the answer
// id (the program-side identities derive from it), and when it was
// submitted. Dispatch is false when nothing needs sending — the same
// answer was already committed.
type AnswerRequest struct {
	RequestID string
	AnswerID  string
	Answers   []ProviderAnswer
	CreatedAt time.Time
	Dispatch  bool
}

// inputEntry is one request the thread knows: its wire shape (without
// the answer, which is joined at read time from the durable answers),
// the private request id, and the provider's question ids by position.
type inputEntry struct {
	request     api.ThreadInputRequest
	requestID   string
	questionIDs []string
}

// ObserveInputs replaces what is pending on a conversation with the
// Integration's current report and settles the answers ATC has sent
// against the evidence reported alongside: a request not seen before
// is pending; a pending one not reported any more is resolved —
// elsewhere, unless the evidence says it was resolved with exactly the
// answer ATC sent; a request already resolved stays so. The settled
// answers persist first, and the fold and the answers then change in
// memory together, so no read sees a request resolved by an answer
// that still reads sent, or the reverse. An answer still sent after
// the fold remains unresolved, which the return value reports so the
// Integration keeps watching. An unmapped identity is dropped.
// Publishes thread.updated when the pending set, a pending request's
// presentation, or an answer's state changed.
func (s *Service) ObserveInputs(ctx context.Context, integrationID, providerID string, pending []InputObservation, resolutions []InputResolution) (bool, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	threadID, known := s.identities[identityKey{integrationID, providerID}]
	if !known {
		s.mu.Unlock()
		s.logger.Debug("input evidence for unmapped conversation dropped", "integration", integrationID)
		return false, nil
	}
	reported := make(map[string]bool, len(pending))
	for _, o := range pending {
		reported[o.RequestID] = true
	}
	updates := s.settleAnswers(threadID, reported, resolutions)
	s.mu.Unlock()
	for _, update := range updates {
		if _, err := s.repository.UpdateAnswer(ctx, update); err != nil {
			return false, err
		}
	}
	s.mu.Lock()
	changed := s.applyInputs(threadID, integrationID, providerID, pending) || len(updates) > 0
	for _, update := range updates {
		s.installAnswer(update)
		if update.State != string(api.InputAnswerResolved) {
			continue
		}
		if entry, err := s.input(threadID, update.RequestID); err == nil {
			if entry.request.Status == api.InputRequestPending {
				resolveInput(entry, api.InputResolvedByAnswer, update.UpdatedAt)
			} else {
				entry.request.Resolution = api.InputResolvedByAnswer
			}
		}
	}
	unresolved := s.unresolvedAnswers(threadID)
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return unresolved, nil
}

// applyInputs folds a report into the thread's entries, reporting
// whether anything changed. Caller holds mu.
func (s *Service) applyInputs(threadID, integrationID, providerID string, pending []InputObservation) bool {
	now := s.now()
	entries := s.inputs[threadID]
	changed := false
	reported := make(map[string]bool, len(pending))
	for _, o := range pending {
		id := ids.Derive(inputPrefix, integrationID+"\x00"+providerID+"\x00"+o.RequestID)
		reported[id] = true
		presented, questionIDs := inputFrom(id, threadID, o)
		index := slices.IndexFunc(entries, func(e *inputEntry) bool { return e.request.ID == id })
		switch {
		case index < 0:
			entries = append(entries, &inputEntry{request: presented, requestID: o.RequestID, questionIDs: questionIDs})
			changed = true
		case entries[index].request.Status == api.InputRequestResolved:
		case !inputEqual(entries[index].request, presented):
			entries[index].request, entries[index].questionIDs = presented, questionIDs
			changed = true
		}
	}
	for _, entry := range entries {
		if entry.request.Status == api.InputRequestPending && !reported[entry.request.ID] {
			resolveInput(entry, api.InputResolvedElsewhere, now)
			changed = true
		}
	}
	s.inputs[threadID] = trimResolvedInputs(entries)
	return changed
}

// settleAnswers matches the thread's unresolved answers against the
// evidence — evidence older than the answer is someone else's —
// returning the records to persist, without changing anything yet. An
// answer whose request the Integration no longer reports (reported, by
// the provider's request id), with no evidence naming it, is
// superseded: the disappearance is not proof the answer did it. Caller
// holds mu.
func (s *Service) settleAnswers(threadID string, reported map[string]bool, resolutions []InputResolution) []store.ThreadAnswerRecord {
	now := s.now()
	var updates []store.ThreadAnswerRecord
	for _, answer := range s.answers[threadID] {
		if answer.State != string(api.InputAnswerSent) {
			continue
		}
		update := *answer
		update.UpdatedAt = now
		settled := false
		for _, evidence := range resolutions {
			if evidence.RequestID != answer.ProviderRequestID || evidence.At.Before(answer.CreatedAt) {
				continue
			}
			switch {
			case evidence.Failure != "" && !evidence.At.Equal(answer.CreatedAt):
				// Another submission's failure: the provider stamps a
				// failure with the command that caused it.
				continue
			case evidence.Failure != "" && evidence.Stale:
				// The request was no longer live: resolved elsewhere, this
				// answer superseded rather than open to another.
				update.State, update.Detail = string(api.InputAnswerSuperseded), evidence.Failure
			case evidence.Failure != "":
				update.State, update.Detail = string(api.InputAnswerFailed), evidence.Failure
			case sameAnswers(decodeProviderAnswers(answer.ProviderAnswers), evidence.Answers):
				update.State = string(api.InputAnswerResolved)
			default:
				update.State, update.Detail = string(api.InputAnswerSuperseded), "the request was resolved with a different answer"
			}
			settled = true
			break
		}
		if !settled && !reported[answer.ProviderRequestID] {
			update.State, update.Detail = string(api.InputAnswerSuperseded), "the request was resolved without evidence of this answer"
			settled = true
		}
		if settled {
			updates = append(updates, update)
		}
	}
	return updates
}

// persistAnswer writes an answer's new state and installs it. Caller
// holds ops, not mu.
func (s *Service) persistAnswer(ctx context.Context, update store.ThreadAnswerRecord) error {
	if _, err := s.repository.UpdateAnswer(ctx, update); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installAnswer(update)
	return nil
}

// installAnswer replaces the in-memory copy of an answer. Caller holds
// mu.
func (s *Service) installAnswer(update store.ThreadAnswerRecord) {
	for _, answer := range s.answers[update.ThreadID] {
		if answer.ID == update.ID {
			*answer = update
		}
	}
}

// latestAnswer is the newest answer submitted through ATC for a request,
// nil for none. Caller holds mu.
func (s *Service) latestAnswer(threadID, requestID string) *store.ThreadAnswerRecord {
	answers := s.answers[threadID]
	for i := len(answers) - 1; i >= 0; i-- {
		if answers[i].RequestID == requestID {
			return answers[i]
		}
	}
	return nil
}

// unresolvedAnswers reports whether an answer on the thread still awaits
// evidence. Caller holds mu.
func (s *Service) unresolvedAnswers(threadID string) bool {
	for _, answer := range s.answers[threadID] {
		if answer.State == string(api.InputAnswerSent) {
			return true
		}
	}
	return false
}

// UnresolvedAnswers lists the provider conversation ids of an
// Integration's threads with an answer still awaiting evidence, so the
// Integration knows what to watch after it connects.
func (s *Service) UnresolvedAnswers(integrationID string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var providerIDs []string
	for threadID := range s.answers {
		if key, ok := s.keys[threadID]; ok && key.integrationID == integrationID && s.unresolvedAnswers(threadID) {
			providerIDs = append(providerIDs, key.providerID)
		}
	}
	slices.Sort(providerIDs)
	return providerIDs
}

// inputFrom builds a request's wire shape from an observation, with
// positional question ids, returning the provider's question ids by
// position alongside.
func inputFrom(id, threadID string, o InputObservation) (api.ThreadInputRequest, []string) {
	request := api.ThreadInputRequest{
		ID: id, ThreadID: threadID, Status: api.InputRequestPending, Questions: make([]api.InputQuestion, 0, len(o.Questions)),
		Unanswerable: o.Unanswerable, RequestedAt: o.RequestedAt,
	}
	questionIDs := make([]string, 0, len(o.Questions))
	for i, q := range o.Questions {
		options := slices.Clone(q.Options)
		if options == nil {
			options = []api.InputOption{}
		}
		request.Questions = append(request.Questions, api.InputQuestion{
			ID: fmt.Sprintf("q%d", i+1), Header: q.Header, Text: q.Text, Options: options, AllowsCustom: q.AllowsCustom, AllowsMultiple: q.AllowsMultiple,
		})
		questionIDs = append(questionIDs, q.ProviderID)
	}
	return request, questionIDs
}

// resolveInput marks a request resolved the given way.
func resolveInput(entry *inputEntry, how api.InputResolution, at time.Time) {
	entry.request.Status = api.InputRequestResolved
	entry.request.Resolution = how
	resolved := at
	entry.request.ResolvedAt = &resolved
}

// trimResolvedInputs keeps every pending request and the newest
// resolvedKept resolved ones.
func trimResolvedInputs(entries []*inputEntry) []*inputEntry {
	resolved := 0
	for _, entry := range entries {
		if entry.request.Status == api.InputRequestResolved {
			resolved++
		}
	}
	excess := resolved - resolvedKept
	if excess <= 0 {
		return entries
	}
	kept := entries[:0:0]
	for _, entry := range entries {
		if entry.request.Status == api.InputRequestResolved && excess > 0 {
			excess--
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func inputEqual(a, b api.ThreadInputRequest) bool {
	return a.ID == b.ID && a.Status == b.Status && a.Unanswerable == b.Unanswerable && a.RequestedAt.Equal(b.RequestedAt) &&
		slices.EqualFunc(a.Questions, b.Questions, func(x, y api.InputQuestion) bool {
			return x.ID == y.ID && x.Header == y.Header && x.Text == y.Text && x.AllowsCustom == y.AllowsCustom &&
				x.AllowsMultiple == y.AllowsMultiple && slices.Equal(x.Options, y.Options)
		})
}

// input finds an entry. Caller holds mu.
func (s *Service) input(threadID, requestID string) (*inputEntry, error) {
	if _, ok := s.view[threadID]; !ok {
		return nil, ErrNotFound
	}
	for _, entry := range s.inputs[threadID] {
		if entry.request.ID == requestID {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrInputNotFound, requestID)
}

// RecoverAnswer finds the answer a submission recovers instead of
// recording one: the newest answer submitted through ATC for the
// request, when it is the same answer and was not refused by the
// provider — sent, resolved, or superseded, its outcome on the record.
// It reports whether there is one; the request says whether it still
// needs dispatching. A different answer while one is sent is refused
// (ErrAnswerPending); one that does not answer the request as the
// record kept it is refused (ErrAnswerInvalid). Nothing is recorded.
func (s *Service) RecoverAnswer(threadID, requestID string, params api.InputAnswerParams) (AnswerRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.view[threadID]; !ok {
		return AnswerRequest{}, false, ErrNotFound
	}
	return s.recoverAnswer(threadID, requestID, params)
}

// recoverAnswer is RecoverAnswer under mu.
func (s *Service) recoverAnswer(threadID, requestID string, params api.InputAnswerParams) (AnswerRequest, bool, error) {
	recorded := s.latestAnswer(threadID, requestID)
	if recorded == nil || recorded.State == string(api.InputAnswerFailed) {
		return AnswerRequest{}, false, nil
	}
	normalized, reply, err := validateAnswers(decodeQuestions(recorded.Questions), params)
	if err != nil {
		return AnswerRequest{}, false, err
	}
	if reply != recorded.Reply || !slices.EqualFunc(decodeAnswers(recorded.Answers), normalized, answerEqual) {
		if recorded.State == string(api.InputAnswerSent) {
			return AnswerRequest{}, false, fmt.Errorf("%w: %s", ErrAnswerPending, requestID)
		}
		return AnswerRequest{}, false, nil
	}
	return AnswerRequest{
		RequestID: recorded.ProviderRequestID, AnswerID: recorded.ID, Answers: decodeProviderAnswers(recorded.ProviderAnswers), CreatedAt: recorded.CreatedAt,
		Dispatch: recorded.State == string(api.InputAnswerSent) && recorded.Delivery == string(api.MessageUncertain),
	}, true, nil
}

// BeginAnswer checks an answer against the request — it must be
// pending and answerable, and the answer must address it in the forms
// it allows, on a thread no stop is being confirmed on — records the
// answer durably, and returns what the Integration needs to dispatch
// it. The same answer begun again recovers the answer already recorded
// (RecoverAnswer) — dispatched again only if its delivery was uncertain
// — whatever the request's state now, and whether or not the
// Integration has reported the request since a restart; a different
// answer is refused while one is sent. After a failed answer the
// request takes another.
func (s *Service) BeginAnswer(ctx context.Context, threadID, requestID string, params api.InputAnswerParams) (AnswerRequest, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	if _, ok := s.view[threadID]; !ok {
		s.mu.Unlock()
		return AnswerRequest{}, ErrNotFound
	}
	if stop := s.stopping(threadID); stop != nil {
		s.mu.Unlock()
		return AnswerRequest{}, fmt.Errorf("%w: %s", ErrThreadStopping, stop.ID)
	}
	if req, found, err := s.recoverAnswer(threadID, requestID, params); err != nil || found {
		s.mu.Unlock()
		return req, err
	}
	entry, err := s.input(threadID, requestID)
	if err != nil {
		s.mu.Unlock()
		return AnswerRequest{}, err
	}
	if entry.request.Status == api.InputRequestResolved {
		s.mu.Unlock()
		return AnswerRequest{}, fmt.Errorf("%w: %s was resolved (%s)", ErrInputResolved, requestID, entry.request.Resolution)
	}
	if entry.request.Unanswerable != "" {
		s.mu.Unlock()
		return AnswerRequest{}, fmt.Errorf("%w: %s", ErrInputUnanswerable, entry.request.Unanswerable)
	}
	questions := entry.request.Questions
	normalized, reply, err := validateAnswers(questions, params)
	if err != nil {
		s.mu.Unlock()
		return AnswerRequest{}, err
	}
	provider := providerAnswers(entry, normalized, reply)
	requestIDPrivate := entry.requestID
	s.mu.Unlock()
	// Millisecond precision: the provider's evidence carries this instant
	// back as it read it from the command, and must compare equal.
	now := s.now().Truncate(time.Millisecond)
	record := store.ThreadAnswerRecord{
		ThreadID: threadID, RequestID: requestID, ProviderRequestID: requestIDPrivate,
		Questions: encode(questions), Answers: encode(normalized), ProviderAnswers: encode(provider), Reply: reply,
		Delivery: string(api.MessageUncertain), State: string(api.InputAnswerSent), CreatedAt: now, UpdatedAt: now,
	}
	for {
		record.ID = ids.NewLong(answerPrefix)
		inserted, err := s.repository.SubmitAnswer(ctx, record, answersKept)
		if err != nil {
			return AnswerRequest{}, err
		}
		if inserted {
			break
		}
	}
	s.mu.Lock()
	answer := record
	s.answers[threadID] = trimAnswers(append(s.answers[threadID], &answer))
	s.mu.Unlock()
	s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	return AnswerRequest{RequestID: requestIDPrivate, AnswerID: answer.ID, Answers: provider, CreatedAt: now, Dispatch: true}, nil
}

// trimAnswers keeps the newest answersKept answers and every unresolved
// one, mirroring the repository's prune.
func trimAnswers(answers []*store.ThreadAnswerRecord) []*store.ThreadAnswerRecord {
	excess := len(answers) - answersKept
	if excess <= 0 {
		return answers
	}
	kept := answers[:0:0]
	for _, answer := range answers {
		if excess > 0 && answer.State != string(api.InputAnswerSent) {
			excess--
			continue
		}
		kept = append(kept, answer)
	}
	return kept
}

// validateAnswers checks an answer against the questions: a reply
// (non-blank, alone, and kept verbatim), or a structured set answering
// at least one question, each at most once, in a form it allows —
// returned in question order.
func validateAnswers(questions []api.InputQuestion, params api.InputAnswerParams) ([]api.QuestionAnswer, string, error) {
	reply := params.Reply
	switch {
	case reply != "" && len(params.Answers) > 0:
		return nil, "", fmt.Errorf("%w: a reply and structured answers together", ErrAnswerInvalid)
	case strings.TrimSpace(reply) != "":
		return []api.QuestionAnswer{}, reply, nil
	case len(params.Answers) == 0:
		return nil, "", fmt.Errorf("%w: nothing answered", ErrAnswerInvalid)
	}
	byID := make(map[string]api.QuestionAnswer, len(params.Answers))
	for _, answer := range params.Answers {
		if _, dup := byID[answer.QuestionID]; dup {
			return nil, "", fmt.Errorf("%w: question %q answered twice", ErrAnswerInvalid, answer.QuestionID)
		}
		byID[answer.QuestionID] = answer
	}
	normalized := make([]api.QuestionAnswer, 0, len(params.Answers))
	for _, question := range questions {
		answer, ok := byID[question.ID]
		if !ok {
			continue
		}
		delete(byID, question.ID)
		text := strings.TrimSpace(answer.Text)
		switch {
		case text != "" && len(answer.Choices) > 0:
			return nil, "", fmt.Errorf("%w: question %s has both choices and text", ErrAnswerInvalid, question.ID)
		case text != "" && !question.AllowsCustom:
			return nil, "", fmt.Errorf("%w: question %s does not allow a custom text", ErrAnswerInvalid, question.ID)
		case text != "":
			normalized = append(normalized, api.QuestionAnswer{QuestionID: question.ID, Text: text})
			continue
		case len(answer.Choices) == 0:
			return nil, "", fmt.Errorf("%w: question %s has no answer", ErrAnswerInvalid, question.ID)
		case len(answer.Choices) > 1 && !question.AllowsMultiple:
			return nil, "", fmt.Errorf("%w: question %s allows one choice", ErrAnswerInvalid, question.ID)
		}
		seen := make(map[string]bool, len(answer.Choices))
		for _, choice := range answer.Choices {
			if seen[choice] {
				return nil, "", fmt.Errorf("%w: question %s chooses %q twice", ErrAnswerInvalid, question.ID, choice)
			}
			seen[choice] = true
			if !slices.ContainsFunc(question.Options, func(o api.InputOption) bool { return o.Value == choice }) {
				return nil, "", fmt.Errorf("%w: question %s does not offer %q", ErrAnswerInvalid, question.ID, choice)
			}
		}
		normalized = append(normalized, api.QuestionAnswer{QuestionID: question.ID, Choices: slices.Clone(answer.Choices)})
	}
	for id := range byID {
		return nil, "", fmt.Errorf("%w: unknown question %q", ErrAnswerInvalid, id)
	}
	return normalized, "", nil
}

// providerAnswers translates a validated answer into the provider's
// terms: a custom text is one value in the single form whatever the
// question allows, as the provider's own surfaces submit it; a reply is
// the text as the first question's custom answer and nothing for the
// others — the one form the providers ATC answers through take a reply
// to a structured request in (the T3 Code evidence is in
// internal/integrations/t3code), never the text duplicated across
// questions. Caller holds mu.
func providerAnswers(entry *inputEntry, answers []api.QuestionAnswer, reply string) []ProviderAnswer {
	if reply != "" {
		return []ProviderAnswer{{QuestionID: entry.questionIDs[0], Values: []string{reply}}}
	}
	provider := make([]ProviderAnswer, 0, len(answers))
	for _, answer := range answers {
		i := slices.IndexFunc(entry.request.Questions, func(q api.InputQuestion) bool { return q.ID == answer.QuestionID })
		if answer.Text != "" {
			provider = append(provider, ProviderAnswer{QuestionID: entry.questionIDs[i], Values: []string{answer.Text}})
			continue
		}
		provider = append(provider, ProviderAnswer{QuestionID: entry.questionIDs[i], Values: slices.Clone(answer.Choices), Multiple: entry.request.Questions[i].AllowsMultiple})
	}
	return provider
}

func answerEqual(a, b api.QuestionAnswer) bool {
	return a.QuestionID == b.QuestionID && a.Text == b.Text && slices.Equal(a.Choices, b.Choices)
}

// sameAnswers reports whether the provider's evidence names exactly the
// answers ATC sent: the same questions, each with the same values in
// any order.
func sameAnswers(ours, theirs []ProviderAnswer) bool {
	if len(ours) != len(theirs) || len(ours) == 0 {
		return false
	}
	values := func(answers []ProviderAnswer) map[string][]string {
		m := make(map[string][]string, len(answers))
		for _, answer := range answers {
			sorted := slices.Clone(answer.Values)
			slices.Sort(sorted)
			m[answer.QuestionID] = sorted
		}
		return m
	}
	mine, other := values(ours), values(theirs)
	if len(mine) != len(other) {
		return false
	}
	for id, v := range mine {
		if !slices.Equal(v, other[id]) {
			return false
		}
	}
	return true
}

// AnswerDelivered records the provider committing an answer: delivery
// accepted, its resolution still awaited.
func (s *Service) AnswerDelivered(ctx context.Context, threadID, answerID string) (api.ThreadInputRequest, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	answer, err := s.answer(threadID, answerID)
	if err != nil {
		return api.ThreadInputRequest{}, err
	}
	if answer.Delivery != string(api.MessageAccepted) {
		answer.Delivery, answer.UpdatedAt = string(api.MessageAccepted), s.now()
		if err := s.persistAnswer(ctx, answer); err != nil {
			return api.ThreadInputRequest{}, err
		}
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return s.InputRequest(threadID, answer.RequestID)
}

// AnswerFailed records the provider refusing an answer for good: the
// answer fails with the provider's reason and the request, still
// pending, takes another. An answer the provider never acknowledged is
// not failed — it stays sent, for the same answers to reconcile.
func (s *Service) AnswerFailed(ctx context.Context, threadID, answerID, reason string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	answer, err := s.answer(threadID, answerID)
	if err != nil {
		return err
	}
	if answer.State != string(api.InputAnswerSent) {
		return nil
	}
	answer.State, answer.Detail, answer.UpdatedAt = string(api.InputAnswerFailed), reason, s.now()
	if err := s.persistAnswer(ctx, answer); err != nil {
		return err
	}
	s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	return nil
}

// answer copies one of the thread's answers out. Caller holds ops.
func (s *Service) answer(threadID, answerID string) (store.ThreadAnswerRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.view[threadID]; !ok {
		return store.ThreadAnswerRecord{}, ErrNotFound
	}
	for _, answer := range s.answers[threadID] {
		if answer.ID == answerID {
			return *answer, nil
		}
	}
	return store.ThreadAnswerRecord{}, fmt.Errorf("%w: answer %s", ErrNotFound, answerID)
}

// InputRequest serves one request as it stands, resolved ones included
// while remembered: from the thread's entries, or — after a restart, or
// once the entry aged out — from the answer submitted through ATC, which
// keeps the questions as they stood.
func (s *Service) InputRequest(threadID, requestID string) (api.ThreadInputRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.input(threadID, requestID)
	if err == nil {
		return s.requestFrom(entry), nil
	}
	answer := s.latestAnswer(threadID, requestID)
	if !errors.Is(err, ErrInputNotFound) || answer == nil {
		return api.ThreadInputRequest{}, err
	}
	request := api.ThreadInputRequest{
		ID: requestID, ThreadID: threadID, Status: api.InputRequestPending, Questions: decodeQuestions(answer.Questions),
		RequestedAt: answer.CreatedAt, Answer: answerFrom(*answer),
	}
	switch answer.State {
	case string(api.InputAnswerResolved):
		resolved := answer.UpdatedAt
		request.Status, request.Resolution, request.ResolvedAt = api.InputRequestResolved, api.InputResolvedByAnswer, &resolved
	case string(api.InputAnswerSuperseded):
		resolved := answer.UpdatedAt
		request.Status, request.Resolution, request.ResolvedAt = api.InputRequestResolved, api.InputResolvedElsewhere, &resolved
	}
	return request, nil
}

// pendingInputs lists a thread's pending requests for the wire. Caller
// holds mu.
func (s *Service) pendingInputs(threadID string) []api.ThreadInputRequest {
	var pending []api.ThreadInputRequest
	for _, entry := range s.inputs[threadID] {
		if entry.request.Status == api.InputRequestPending {
			pending = append(pending, s.requestFrom(entry))
		}
	}
	return pending
}

// closeInputs resolves every pending request by a stop and supersedes
// the answers still awaiting evidence, in memory — the repository
// committed the same in the stop's transaction (ResolveStop). Caller
// holds mu.
func (s *Service) closeInputs(threadID, supersession string, at time.Time) {
	for _, entry := range s.inputs[threadID] {
		if entry.request.Status == api.InputRequestPending {
			resolveInput(entry, api.InputResolvedByStop, at)
		}
	}
	s.supersedeAnswers(threadID, supersession, at)
}

// supersedeAnswers marks every answer still awaiting evidence superseded
// in memory. Caller holds mu.
func (s *Service) supersedeAnswers(threadID, supersession string, at time.Time) {
	for _, answer := range s.answers[threadID] {
		if answer.State == string(api.InputAnswerSent) {
			answer.State, answer.Detail, answer.UpdatedAt = string(api.InputAnswerSuperseded), supersession, at
		}
	}
}

// requestFrom is an entry's wire shape with its newest answer joined.
// Caller holds mu.
func (s *Service) requestFrom(entry *inputEntry) api.ThreadInputRequest {
	request := entry.request
	request.Questions = make([]api.InputQuestion, len(entry.request.Questions))
	for i, question := range entry.request.Questions {
		question.Options = slices.Clone(question.Options)
		request.Questions[i] = question
	}
	if request.ResolvedAt != nil {
		at := *request.ResolvedAt
		request.ResolvedAt = &at
	}
	if answer := s.latestAnswer(entry.request.ThreadID, entry.request.ID); answer != nil {
		request.Answer = answerFrom(*answer)
	}
	return request
}

func answerFrom(record store.ThreadAnswerRecord) *api.InputAnswer {
	return &api.InputAnswer{
		Answers: decodeAnswers(record.Answers), Reply: record.Reply, Delivery: api.MessageDelivery(record.Delivery), State: api.InputAnswerState(record.State),
		Detail: record.Detail, SubmittedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
	}
}

// The JSON columns: encoded here, decoded leniently — a column that
// does not decode is treated as empty rather than failing a read.
func encode(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func decodeAnswers(data string) []api.QuestionAnswer {
	var answers []api.QuestionAnswer
	_ = json.Unmarshal([]byte(data), &answers)
	if answers == nil {
		answers = []api.QuestionAnswer{}
	}
	return answers
}

func decodeProviderAnswers(data string) []ProviderAnswer {
	var answers []ProviderAnswer
	_ = json.Unmarshal([]byte(data), &answers)
	return answers
}

func decodeQuestions(data string) []api.InputQuestion {
	var questions []api.InputQuestion
	_ = json.Unmarshal([]byte(data), &questions)
	if questions == nil {
		questions = []api.InputQuestion{}
	}
	return questions
}
