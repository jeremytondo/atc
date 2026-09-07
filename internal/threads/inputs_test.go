package threads

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
)

var inputIDPattern = regexp.MustCompile(`^inpt-[23456789bcdfghjkmnpqrstvwxyz]{10}$`)

// twoQuestions is a request with a single-choice question and a
// multi-select one that also takes custom text.
func twoQuestions(requestID string, at time.Time) InputObservation {
	return InputObservation{RequestID: requestID, RequestedAt: at, Questions: []QuestionObservation{
		{ProviderID: "Which color?", Header: "Color", Text: "Which color?", Options: []api.InputOption{{Value: "Red", Label: "Red", Description: "warm"}, {Value: "Blue", Label: "Blue"}}, AllowsCustom: false},
		{ProviderID: "tools", Header: "Tools", Text: "Which tools?", Options: []api.InputOption{{Value: "go", Label: "Go"}, {Value: "make", Label: "Make"}}, AllowsCustom: true, AllowsMultiple: true},
	}}
}

// structured wraps answers as an answer submission.
func structured(answers ...api.QuestionAnswer) api.InputAnswerParams {
	return api.InputAnswerParams{Answers: answers}
}

// external observes a T3 thread into the fixture and returns its id.
func (f *fixture) external(t *testing.T, providerID string, status api.ThreadStatus) string {
	t.Helper()
	id, err := f.service.ObserveExternal(context.Background(), ExternalObservation{
		IntegrationID: "t3code", ProviderID: providerID, InitialDirectory: f.dir("proj-aaaaa"), Status: status, Title: "T",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	return id
}

// Pending structured requests (ATC-308) ride the thread with stable,
// derived ids and positional question ids: a report adds and updates
// them and publishes thread.updated whether or not the status changed;
// a request no longer reported is resolved elsewhere and stays resolved
// whatever a later report says; content ATC cannot answer is shown with
// its reason; an unmapped identity is dropped; and T3 dropping the
// thread clears them.
func TestObserveInputs(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.external(t, "t1", api.ThreadWaitingForInput)
	requested := time.Date(2026, 9, 1, 0, 0, 5, 0, time.UTC)
	first := twoQuestions("req-1", requested)
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{first}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if len(thread.InputRequests) != 1 || !inputIDPattern.MatchString(thread.InputRequests[0].ID) {
		t.Fatalf("input requests = %+v", thread.InputRequests)
	}
	requestID := thread.InputRequests[0].ID
	want := api.ThreadInputRequest{ID: requestID, ThreadID: id, Status: api.InputRequestPending, RequestedAt: requested, Questions: []api.InputQuestion{
		{ID: "q1", Header: "Color", Text: "Which color?", Options: []api.InputOption{{Value: "Red", Label: "Red", Description: "warm"}, {Value: "Blue", Label: "Blue"}}},
		{ID: "q2", Header: "Tools", Text: "Which tools?", Options: []api.InputOption{{Value: "go", Label: "Go"}, {Value: "make", Label: "Make"}}, AllowsCustom: true, AllowsMultiple: true},
	}}
	if diff := cmp.Diff(want, thread.InputRequests[0]); diff != "" {
		t.Errorf("request (-want +got):\n%s", diff)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a new request = %v", got)
	}
	// The same report again changes nothing; a second, unanswerable
	// request joins with its reason.
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{first}, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on an unchanged report = %v", got)
	}
	second := InputObservation{RequestID: "req-2", RequestedAt: requested.Add(time.Second), Unanswerable: "question 1 offers no choices and allows no custom text",
		Questions: []QuestionObservation{{ProviderID: "x", Text: "Pick", AllowsCustom: false}}}
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{first, second}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if len(thread.InputRequests) != 2 || thread.InputRequests[0].ID != requestID || thread.InputRequests[1].Unanswerable == "" || len(thread.InputRequests[1].Questions[0].Options) != 0 {
		t.Errorf("requests = %+v", thread.InputRequests)
	}
	if _, err := f.service.BeginAnswer(ctx, id, thread.InputRequests[1].ID, structured(api.QuestionAnswer{QuestionID: "q1", Text: "x"})); !errors.Is(err, ErrInputUnanswerable) {
		t.Errorf("answering an unanswerable request = %v", err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a second request = %v", got)
	}
	// The first is resolved elsewhere: gone from the thread, remembered
	// as resolved, readable, and not revived by a stale report.
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{second}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if len(thread.InputRequests) != 1 || thread.InputRequests[0].ID == requestID {
		t.Errorf("requests after resolution elsewhere = %+v", thread.InputRequests)
	}
	resolved, err := f.service.InputRequest(id, requestID)
	if err != nil || resolved.Status != api.InputRequestResolved || resolved.Resolution != api.InputResolvedElsewhere || resolved.ResolvedAt == nil {
		t.Errorf("resolved request = %+v, %v", resolved, err)
	}
	if _, err := f.service.BeginAnswer(ctx, id, requestID, structured(api.QuestionAnswer{QuestionID: "q1", Choices: []string{"Red"}}, api.QuestionAnswer{QuestionID: "q2", Choices: []string{"go"}})); !errors.Is(err, ErrInputResolved) || !strings.Contains(err.Error(), "elsewhere") {
		t.Errorf("answer on a request resolved elsewhere = %v", err)
	}
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{first, second}, nil); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.InputRequests) != 1 {
		t.Errorf("stale report revived a resolved request: %+v", thread.InputRequests)
	}
	f.drain()
	// A pending request's presentation can change.
	richer := second
	richer.Questions[0].Options = []api.InputOption{{Value: "a", Label: "a"}}
	richer.Unanswerable = ""
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{richer}, nil); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); thread.InputRequests[0].Unanswerable != "" || len(thread.InputRequests[0].Questions[0].Options) != 1 {
		t.Errorf("updated presentation = %+v", thread.InputRequests[0])
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a presentation change = %v", got)
	}
	if _, err := f.service.InputRequest(id, "inpt-nope"); !errors.Is(err, ErrInputNotFound) {
		t.Errorf("unknown request = %v", err)
	}
	if _, err := f.service.InputRequest("thrd-nope", requestID); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown thread = %v", err)
	}
	if unresolved, err := f.service.ObserveInputs(ctx, "t3code", "t-unknown", []InputObservation{first}, nil); err != nil || unresolved {
		t.Errorf("unmapped identity = %v, %v", unresolved, err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events for an unmapped identity = %v", got)
	}
	if err := f.service.ArchiveExternalThread(ctx, "t3code", "t1"); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.InputRequests) != 0 || !thread.Archived {
		t.Errorf("after T3 dropped the thread = %+v", thread)
	}
}

// An answer must address the request: each question at most once, in a
// form it allows, with choices it offers; a subset of the questions, or
// a reply alone, is an answer too (ATC-309).
func TestBeginAnswerValidation(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.external(t, "t1", api.ThreadWaitingForInput)
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{twoQuestions("req-1", f.clock.Now())}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	requestID := thread.InputRequests[0].ID
	cases := map[string][]api.QuestionAnswer{
		"nothing":           {},
		"unknown":           {{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Choices: []string{"go"}}, {QuestionID: "q9", Text: "x"}},
		"twice":             {{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q1", Choices: []string{"Blue"}}, {QuestionID: "q2", Choices: []string{"go"}}},
		"not offered":       {{QuestionID: "q1", Choices: []string{"Green"}}, {QuestionID: "q2", Choices: []string{"go"}}},
		"several on single": {{QuestionID: "q1", Choices: []string{"Red", "Blue"}}, {QuestionID: "q2", Choices: []string{"go"}}},
		"text not allowed":  {{QuestionID: "q1", Text: "Green"}, {QuestionID: "q2", Choices: []string{"go"}}},
		"both forms":        {{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Choices: []string{"go"}, Text: "x"}},
		"empty":             {{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2"}},
		"blank text":        {{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Text: "  "}},
		"duplicate choice":  {{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Choices: []string{"go", "go"}}},
	}
	for name, answers := range cases {
		if _, err := f.service.BeginAnswer(ctx, id, requestID, structured(answers...)); !errors.Is(err, ErrAnswerInvalid) {
			t.Errorf("%s = %v; want ErrAnswerInvalid", name, err)
		}
	}
	for name, params := range map[string]api.InputAnswerParams{
		"blank reply":             {Reply: "  "},
		"reply and answers":       {Reply: "Blue", Answers: []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Red"}}}},
		"blank reply and answers": {Reply: " ", Answers: []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Red"}}}},
	} {
		if _, err := f.service.BeginAnswer(ctx, id, requestID, params); !errors.Is(err, ErrAnswerInvalid) {
			t.Errorf("%s = %v; want ErrAnswerInvalid", name, err)
		}
	}
	if _, err := f.service.BeginAnswer(ctx, id, "inpt-nope", api.InputAnswerParams{}); !errors.Is(err, ErrInputNotFound) {
		t.Errorf("unknown request = %v", err)
	}
	if got, _ := f.service.Get(id); got.InputRequests[0].Answer != nil {
		t.Error("a refused answer set was recorded")
	}
	// The valid forms: one choice, several choices, custom text
	// (trimmed); the provider's terms follow the provider's question ids
	// and the multiple-selection shape.
	req, err := f.service.BeginAnswer(ctx, id, requestID, structured(api.QuestionAnswer{QuestionID: "q2", Text: " vim "}, api.QuestionAnswer{QuestionID: "q1", Choices: []string{"Blue"}}))
	if err != nil {
		t.Fatal(err)
	}
	want := []ProviderAnswer{{QuestionID: "Which color?", Values: []string{"Blue"}}, {QuestionID: "tools", Values: []string{"vim"}}}
	if diff := cmp.Diff(want, req.Answers); diff != "" || req.RequestID != "req-1" || !req.Dispatch || !strings.HasPrefix(req.AnswerID, "ans-") {
		t.Errorf("answer request = %+v (-want +got):\n%s", req, diff)
	}
	if got, _ := f.service.Get(id); got.InputRequests[0].Answer == nil || got.InputRequests[0].Answer.State != api.InputAnswerSent || got.InputRequests[0].Answer.Answers[1].Text != "vim" {
		t.Errorf("recorded answer = %+v", got.InputRequests[0].Answer)
	}
	// A subset of the questions is an answer (ATC-309): the provider
	// forwards what is answered. A reply is the user's text alone, with
	// the provider's question ids for the Integration's translation.
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{twoQuestions("req-1", f.clock.Now()), twoQuestions("req-2", f.clock.Now()), twoQuestions("req-3", f.clock.Now())}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	partial, err := f.service.BeginAnswer(ctx, id, thread.InputRequests[1].ID, structured(api.QuestionAnswer{QuestionID: "q2", Choices: []string{"make"}}))
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]ProviderAnswer{{QuestionID: "tools", Values: []string{"make"}, Multiple: true}}, partial.Answers); diff != "" {
		t.Errorf("partial answer = %+v (-want +got):\n%s", partial, diff)
	}
	reply, err := f.service.BeginAnswer(ctx, id, thread.InputRequests[2].ID, api.InputAnswerParams{Reply: " Blue, and just go for now "})
	if err != nil {
		t.Fatal(err)
	}
	// Verbatim, whitespace included, as the first question's custom
	// answer and nothing for the others.
	if diff := cmp.Diff([]ProviderAnswer{{QuestionID: "Which color?", Values: []string{" Blue, and just go for now "}}}, reply.Answers); diff != "" || !reply.Dispatch {
		t.Errorf("reply = %+v (-want +got):\n%s", reply, diff)
	}
	if got, _ := f.service.InputRequest(id, thread.InputRequests[2].ID); got.Answer == nil || got.Answer.Reply != " Blue, and just go for now " || len(got.Answer.Answers) != 0 {
		t.Errorf("recorded reply = %+v", got.Answer)
	}
	// The same reply recovers it; a different one is refused while it
	// awaits evidence; the evidence naming the reply as sent resolves it.
	if again, err := f.service.BeginAnswer(ctx, id, thread.InputRequests[2].ID, api.InputAnswerParams{Reply: " Blue, and just go for now "}); err != nil || again.AnswerID != reply.AnswerID {
		t.Errorf("same reply = %+v, %v", again, err)
	}
	if _, err := f.service.BeginAnswer(ctx, id, thread.InputRequests[2].ID, api.InputAnswerParams{Reply: "Red"}); !errors.Is(err, ErrAnswerPending) {
		t.Errorf("different reply while sent = %v", err)
	}
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", nil, []InputResolution{{RequestID: "req-3", Answers: []ProviderAnswer{{QuestionID: "Which color?", Values: []string{" Blue, and just go for now "}}}, At: f.clock.Now()}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.service.InputRequest(id, thread.InputRequests[2].ID); got.Resolution != api.InputResolvedByAnswer || got.Answer.State != api.InputAnswerResolved {
		t.Errorf("reply resolved = %+v (answer %+v)", got, got.Answer)
	}
}

// An answer's outcomes (ATC-308): sent until the provider's evidence;
// resolved when the request resolved with exactly these answers;
// superseded when it resolved with others, or vanished without any;
// failed — the request still pending and taking another — when the
// provider reports a failure no older than the answer, an older one
// being someone else's. A retry recovers the same answer, re-dispatched
// only while its delivery is uncertain; a different answer meanwhile is
// refused.
func TestAnswerOutcomes(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.external(t, "t1", api.ThreadWaitingForInput)
	requested := f.clock.Now()
	report := func(requests ...string) []InputObservation {
		var pending []InputObservation
		for _, request := range requests {
			pending = append(pending, twoQuestions(request, requested))
		}
		return pending
	}
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", report("req-1", "req-2", "req-3", "req-4"), nil); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	ids := map[string]string{}
	for i, request := range thread.InputRequests {
		ids[[]string{"req-1", "req-2", "req-3", "req-4"}[i]] = request.ID
	}
	answers := []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Choices: []string{"go", "make"}}}
	begin := func(request string) AnswerRequest {
		t.Helper()
		req, err := f.service.BeginAnswer(ctx, id, ids[request], structured(answers...))
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	// Someone else's failure, older than the answer, is not the answer's;
	// nor is one stamped with another instant.
	older := InputResolution{RequestID: "req-1", Failure: "No active provider session is bound to this thread.", At: f.clock.Now()}
	first := begin("req-1")
	f.drain()
	// A retry of the same answers while uncertain dispatches again under
	// the same id; a different set is refused; once delivered, the same
	// set is returned without a dispatch.
	if again, err := f.service.BeginAnswer(ctx, id, ids["req-1"], structured(answers...)); err != nil || again.AnswerID != first.AnswerID || !again.Dispatch {
		t.Errorf("retry while uncertain = %+v, %v", again, err)
	}
	if _, err := f.service.BeginAnswer(ctx, id, ids["req-1"], structured(api.QuestionAnswer{QuestionID: "q1", Choices: []string{"Blue"}}, api.QuestionAnswer{QuestionID: "q2", Choices: []string{"go"}})); !errors.Is(err, ErrAnswerPending) {
		t.Errorf("different answer while sent = %v", err)
	}
	if request, err := f.service.AnswerDelivered(ctx, id, first.AnswerID); err != nil || request.Answer.Delivery != api.MessageAccepted || request.Answer.State != api.InputAnswerSent {
		t.Errorf("AnswerDelivered = %+v, %v", request, err)
	}
	if again, err := f.service.BeginAnswer(ctx, id, ids["req-1"], structured(answers...)); err != nil || again.AnswerID != first.AnswerID || again.Dispatch {
		t.Errorf("retry once delivered = %+v, %v", again, err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on delivery = %v", got)
	}
	// Evidence: req-1 resolved with our answers (in another order);
	// req-2 resolved with someone else's; req-3 failed for good; req-4
	// gone without a word.
	second, third, fourth := begin("req-2"), begin("req-3"), begin("req-4")
	f.drain()
	later := f.clock.Now()
	ours := []ProviderAnswer{{QuestionID: "tools", Values: []string{"make", "go"}, Multiple: true}, {QuestionID: "Which color?", Values: []string{"Red"}}}
	unresolved, err := f.service.ObserveInputs(ctx, "t3code", "t1", report("req-3"), []InputResolution{
		older,
		{RequestID: "req-1", Answers: ours, At: later},
		{RequestID: "req-2", Answers: []ProviderAnswer{{QuestionID: "Which color?", Values: []string{"Blue"}}, {QuestionID: "tools", Values: []string{"go"}, Multiple: true}}, At: later},
		{RequestID: "req-3", Failure: "No active provider session is bound to this thread.", At: later},
		{RequestID: "req-3", Failure: "No active provider session is bound to this thread.", At: third.CreatedAt},
	})
	if err != nil || unresolved {
		t.Fatalf("ObserveInputs = %v, %v", unresolved, err)
	}
	for _, c := range []struct {
		request    string
		answerID   string
		state      api.InputAnswerState
		status     api.InputRequestStatus
		resolution api.InputResolution
	}{
		{"req-1", first.AnswerID, api.InputAnswerResolved, api.InputRequestResolved, api.InputResolvedByAnswer},
		{"req-2", second.AnswerID, api.InputAnswerSuperseded, api.InputRequestResolved, api.InputResolvedElsewhere},
		{"req-3", third.AnswerID, api.InputAnswerFailed, api.InputRequestPending, ""},
		{"req-4", fourth.AnswerID, api.InputAnswerSuperseded, api.InputRequestResolved, api.InputResolvedElsewhere},
	} {
		request, err := f.service.InputRequest(id, ids[c.request])
		if err != nil || request.Answer == nil || request.Answer.State != c.state || request.Status != c.status || request.Resolution != c.resolution {
			t.Errorf("%s = %+v (answer %+v), %v; want %s %s %q", c.request, request, request.Answer, err, c.state, c.status, c.resolution)
		}
	}
	if request, _ := f.service.InputRequest(id, ids["req-3"]); !strings.Contains(request.Answer.Detail, "No active provider session") {
		t.Errorf("failed answer detail = %q", request.Answer.Detail)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on evidence = %v", got)
	}
	// The failed request takes another answer; the same answers on the
	// resolved request recover the resolved answer, others are refused.
	if req, err := f.service.BeginAnswer(ctx, id, ids["req-3"], structured(answers...)); err != nil || req.AnswerID == third.AnswerID {
		t.Errorf("answer after failure = %+v, %v", req, err)
	}
	if req, err := f.service.BeginAnswer(ctx, id, ids["req-1"], structured(answers...)); err != nil || req.AnswerID != first.AnswerID || req.Dispatch {
		t.Errorf("same answers on a resolved request = %+v, %v", req, err)
	}
	if _, err := f.service.BeginAnswer(ctx, id, ids["req-1"], structured(api.QuestionAnswer{QuestionID: "q1", Choices: []string{"Blue"}}, api.QuestionAnswer{QuestionID: "q2", Choices: []string{"go"}})); !errors.Is(err, ErrInputResolved) {
		t.Errorf("other answers on a resolved request = %v", err)
	}
	// A stale failure — the request no longer live — supersedes the
	// answer and the request reads resolved elsewhere, restart or not.
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", report("req-3", "req-6"), nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	ids["req-6"] = thread.InputRequests[len(thread.InputRequests)-1].ID
	sixth := begin("req-6")
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", report("req-3"), []InputResolution{{RequestID: "req-6", Failure: "Stale pending user-input request: req-6.", Stale: true, At: sixth.CreatedAt}}); err != nil {
		t.Fatal(err)
	}
	if request, _ := f.service.InputRequest(id, ids["req-6"]); request.Answer.State != api.InputAnswerSuperseded || request.Resolution != api.InputResolvedElsewhere {
		t.Errorf("stale failure = %+v (answer %+v)", request, request.Answer)
	}
	stale := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: events.NewHubAt(8, 1), Now: f.clock.Now})
	if err := stale.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if request, _ := stale.InputRequest(id, ids["req-6"]); request.Status != api.InputRequestResolved || request.Resolution != api.InputResolvedElsewhere {
		t.Errorf("stale failure after reload = %+v", request)
	}
	// Someone's answer to an unanswered request is resolution elsewhere.
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", report("req-5"), nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	fifth := thread.InputRequests[len(thread.InputRequests)-1].ID
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", nil, []InputResolution{{RequestID: "req-5", Answers: ours, At: f.clock.Now()}}); err != nil {
		t.Fatal(err)
	}
	if request, _ := f.service.InputRequest(id, fifth); request.Resolution != api.InputResolvedElsewhere || request.Answer != nil {
		t.Errorf("unanswered request resolved = %+v", request)
	}
}

// An answer survives a restart (ATC-308): the reloaded service holds it
// sent, serves its request from the record while the Integration has
// not reported the request again, names the thread as awaiting
// evidence, links the request back when it is reported, and resolves
// the answer on the evidence.
func TestAnswerSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.external(t, "t1", api.ThreadWaitingForInput)
	requested := f.clock.Now()
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{twoQuestions("req-1", requested)}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	requestID := thread.InputRequests[0].ID
	answers := []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Text: "vim"}}
	req, err := f.service.BeginAnswer(ctx, id, requestID, structured(answers...))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.AnswerDelivered(ctx, id, req.AnswerID); err != nil {
		t.Fatal(err)
	}

	reloaded := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: events.NewHubAt(8, 1), Now: f.clock.Now})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got := reloaded.UnresolvedAnswers("t3code"); !slices.Equal(got, []string{"t1"}) {
		t.Errorf("UnresolvedAnswers = %v", got)
	}
	request, err := reloaded.InputRequest(id, requestID)
	if err != nil || request.Status != api.InputRequestPending || request.Answer == nil || request.Answer.State != api.InputAnswerSent || len(request.Questions) != 2 || request.Questions[1].AllowsMultiple != true {
		t.Fatalf("request after reload = %+v (answer %+v), %v", request, request.Answer, err)
	}
	// Before the Integration reports the request again, the durable
	// answer stands in: the same answers recover it, validated against
	// the questions it kept; different ones are refused.
	if again, err := reloaded.BeginAnswer(ctx, id, requestID, structured(answers...)); err != nil || again.AnswerID != req.AnswerID || again.Dispatch || again.RequestID != "req-1" {
		t.Errorf("retry before the request is reported again = %+v, %v", again, err)
	}
	if _, err := reloaded.BeginAnswer(ctx, id, requestID, structured(api.QuestionAnswer{QuestionID: "q1", Choices: []string{"Blue"}}, api.QuestionAnswer{QuestionID: "q2", Text: "vim"})); !errors.Is(err, ErrAnswerPending) {
		t.Errorf("different answer before the request is reported again = %v", err)
	}
	if _, err := reloaded.BeginAnswer(ctx, id, requestID, structured(api.QuestionAnswer{QuestionID: "q1", Choices: []string{"Green"}}, api.QuestionAnswer{QuestionID: "q2", Text: "vim"})); !errors.Is(err, ErrAnswerInvalid) {
		t.Errorf("invalid answer before the request is reported again = %v", err)
	}
	if _, err := reloaded.BeginAnswer(ctx, id, "inpt-nope", structured(answers...)); !errors.Is(err, ErrInputNotFound) {
		t.Errorf("unknown request after reload = %v", err)
	}
	if _, err := reloaded.ObserveInputs(ctx, "t3code", "t1", []InputObservation{twoQuestions("req-1", requested)}, nil); err != nil {
		t.Fatal(err)
	}
	if again, err := reloaded.BeginAnswer(ctx, id, requestID, structured(answers...)); err != nil || again.AnswerID != req.AnswerID || again.Dispatch {
		t.Errorf("retry after reload = %+v, %v", again, err)
	}
	unresolved, err := reloaded.ObserveInputs(ctx, "t3code", "t1", nil, []InputResolution{{RequestID: "req-1", Answers: req.Answers, At: f.clock.Now()}})
	if err != nil || unresolved {
		t.Fatalf("evidence after reload = %v, %v", unresolved, err)
	}
	if request, _ = reloaded.InputRequest(id, requestID); request.Resolution != api.InputResolvedByAnswer || request.Answer.State != api.InputAnswerResolved {
		t.Errorf("resolved after reload = %+v (answer %+v)", request, request.Answer)
	}
	if got := reloaded.UnresolvedAnswers("t3code"); len(got) != 0 {
		t.Errorf("UnresolvedAnswers after resolution = %v", got)
	}
	// Another answer sent before a restart, whose request the program
	// no longer reports and no evidence names: superseded, not sent
	// forever.
	if _, err := reloaded.ObserveInputs(ctx, "t3code", "t1", []InputObservation{twoQuestions("req-2", f.clock.Now())}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = reloaded.Get(id)
	second, err := reloaded.BeginAnswer(ctx, id, thread.InputRequests[0].ID, structured(answers...))
	if err != nil {
		t.Fatal(err)
	}
	again := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: events.NewHubAt(8, 1), Now: f.clock.Now})
	if err := again.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if unresolved, err := again.ObserveInputs(ctx, "t3code", "t1", nil, nil); err != nil || unresolved {
		t.Errorf("ObserveInputs after the second reload = %v, %v", unresolved, err)
	}
	if request, _ := again.InputRequest(id, thread.InputRequests[0].ID); request.Answer == nil || request.Answer.State != api.InputAnswerSuperseded || request.Resolution != api.InputResolvedElsewhere {
		t.Errorf("answer whose request vanished = %+v", request)
	}
	_ = second
}
