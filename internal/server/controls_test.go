package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
)

func decodeInputRequest(t *testing.T, rec *httptest.ResponseRecorder) api.ThreadInputRequest {
	t.Helper()
	var request api.ThreadInputRequest
	if err := json.Unmarshal(rec.Body.Bytes(), &request); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return request
}

func decodeStop(t *testing.T, rec *httptest.ResponseRecorder) api.ThreadStop {
	t.Helper()
	var stop api.ThreadStop
	if err := json.Unmarshal(rec.Body.Bytes(), &stop); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return stop
}

// asking has the fake T3 report a Codex thread blocked on one
// two-question request, and returns ATC's record with the request on
// it.
func (f *fixture) asking(t *testing.T, sequence uint64) api.Thread {
	t.Helper()
	requested := t3codetest.UserInputRequested("a1", "req-1",
		t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Red", "warm"), t3codetest.QuestionOption("Blue", "cool"), t3codetest.AllowCustom(false)),
		t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("go", "Go"), t3codetest.QuestionOption("make", "Make"), t3codetest.MultiSelect()))
	f.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")), requested))
	thread := f.t3Thread(t, sequence, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(false, true))
	waitUntil(t, "the request on the thread", func() bool { thread, _ = f.threads.Get(thread.ID); return len(thread.InputRequests) == 1 })
	return thread
}

// The answer path over the wire (ATC-308): the request rides the thread
// with positional question ids and explicit allowances; an answer set
// returns 202 with the answer recorded and delivered, T3 received one
// thread.user-input.respond in its shape, the same answers again send
// nothing more, different ones are refused, and the request stays
// pending until T3's evidence resolves it with these answers — then
// the request reads resolved by answer, the thread no longer lists it,
// and answering again is refused.
func TestThreadAnswerOverTheWire(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.asking(t, 2)
	request := thread.InputRequests[0]
	if thread.Status != api.ThreadWaitingForInput || request.Questions[0].ID != "q1" || request.Questions[0].AllowsCustom || !request.Questions[1].AllowsMultiple || request.Unanswerable != "" {
		t.Fatalf("thread = %s, request %+v", thread.Status, request)
	}
	path := "/v1/threads/" + thread.ID + "/input-requests/" + request.ID
	rec := f.request(t, http.MethodGet, path, "")
	if rec.Code != http.StatusOK || decodeInputRequest(t, rec).ID != request.ID {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)

	body := `{"answers":[{"questionId":"q1","choices":["Blue"]},{"questionId":"q2","choices":["go","make"]}]}`
	rec = f.request(t, http.MethodPost, path+"/answer", body)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("answer: got %d; body %s", rec.Code, rec.Body)
	}
	answered := decodeInputRequest(t, rec)
	if answered.Status != api.InputRequestPending || answered.Answer == nil || answered.Answer.Delivery != api.MessageAccepted || answered.Answer.State != api.InputAnswerSent {
		t.Fatalf("answered = %+v (answer %+v)", answered, answered.Answer)
	}
	commands := f.t3Server.Commands()
	if len(commands) != 1 || commands[0]["type"] != "thread.user-input.respond" || commands[0]["requestId"] != "req-1" ||
		commands[0]["answers"].(map[string]any)["color"] != "Blue" || len(commands[0]["answers"].(map[string]any)["tools"].([]any)) != 2 {
		t.Errorf("commands = %v", commands)
	}
	if got := changes(sub); len(got) < 1 || got[0] != "thread.updated "+thread.ID {
		t.Errorf("events on answer = %v", got)
	}
	// The same answers again: nothing more sent. Different ones: refused.
	rec = f.request(t, http.MethodPost, path+"/answer", body)
	if rec.Code != http.StatusAccepted || len(f.t3Server.Commands()) != 1 {
		t.Errorf("replay: %d, %d commands", rec.Code, len(f.t3Server.Commands()))
	}
	rec = f.request(t, http.MethodPost, path+"/answer", `{"answers":[{"questionId":"q1","choices":["Red"]},{"questionId":"q2","choices":["go"]}]}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusConflict || problem.Code != api.CodeInputAnswerPending {
		t.Errorf("different answer: %d %+v", rec.Code, problem)
	}
	// T3 shows the request resolved with these answers.
	f.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.UserInputRequested("a1", "req-1", t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Red", "")), t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("go", ""), t3codetest.MultiSelect())),
		t3codetest.UserInputResolved("b2", "req-1", map[string]any{"color": "Blue", "tools": []any{"go", "make"}})))
	f.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))))
	var resolved api.ThreadInputRequest
	waitUntil(t, "the request resolved", func() bool {
		resolved = decodeInputRequest(t, f.request(t, http.MethodGet, path, ""))
		return resolved.Status == api.InputRequestResolved
	})
	if resolved.Resolution != api.InputResolvedByAnswer || resolved.Answer.State != api.InputAnswerResolved || resolved.ResolvedAt == nil {
		t.Errorf("resolved = %+v (answer %+v)", resolved, resolved.Answer)
	}
	if after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")); len(after.InputRequests) != 0 || after.Status != api.ThreadWorking {
		t.Errorf("thread after resolution = %+v", after)
	}
	// The same answers again recover the resolved answer (a retry whose
	// first response was lost); others are refused: the request is done.
	if rec = f.request(t, http.MethodPost, path+"/answer", body); rec.Code != http.StatusAccepted || decodeInputRequest(t, rec).Answer.State != api.InputAnswerResolved {
		t.Errorf("same answers after resolution: %d %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodPost, path+"/answer", `{"answers":[{"questionId":"q1","choices":["Red"]},{"questionId":"q2","choices":["go"]}]}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusConflict || problem.Code != api.CodeInputRequestResolved {
		t.Errorf("other answers after resolution: %d %+v", rec.Code, problem)
	}
}

// Every refusal of an answer, by name, and none of them reaching T3;
// and T3's rejection failing the answer while the request takes
// another.
func TestThreadAnswerRefusals(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.asking(t, 2)
	request := thread.InputRequests[0]
	path := "/v1/threads/" + thread.ID + "/input-requests/" + request.ID
	terminal := f.createRunningTerminal(t)
	claude := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)
	cases := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
	}{
		{"unknown thread", "/v1/threads/thrd-nope/input-requests/" + request.ID, `{"answers":[]}`, http.StatusNotFound, api.CodeThreadNotFound},
		{"unknown request", "/v1/threads/" + thread.ID + "/input-requests/inpt-nope", `{"answers":[]}`, http.StatusNotFound, api.CodeInputRequestNotFound},
		{"integration cannot answer", "/v1/threads/" + claude + "/input-requests/" + request.ID, `{"answers":[]}`, http.StatusBadRequest, api.CodeThreadAnswerUnsupported},
		{"nothing answered", path, `{"answers":[]}`, http.StatusBadRequest, api.CodeInputAnswerInvalid},
		{"reply with answers", path, `{"reply":"Red","answers":[{"questionId":"q2","choices":["go"]}]}`, http.StatusBadRequest, api.CodeInputAnswerInvalid},
		{"custom text refused", path, `{"answers":[{"questionId":"q1","text":"Green"},{"questionId":"q2","choices":["go"]}]}`, http.StatusBadRequest, api.CodeInputAnswerInvalid},
		{"choice not offered", path, `{"answers":[{"questionId":"q1","choices":["Green"]},{"questionId":"q2","choices":["go"]}]}`, http.StatusBadRequest, api.CodeInputAnswerInvalid},
	}
	for _, c := range cases {
		rec := f.request(t, http.MethodPost, c.path+"/answer", c.body)
		if problem := decodeProblem(t, rec); rec.Code != c.status || problem.Code != c.code {
			t.Errorf("%s: got %d %+v; want %d %s", c.name, rec.Code, problem, c.status, c.code)
		}
	}
	if rec := f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/input-requests/inpt-nope", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown request: %d", rec.Code)
	}
	if len(f.t3Server.Commands()) != 0 {
		t.Errorf("refusals reached T3: %v", f.t3Server.Commands())
	}
	// T3 rejects the answer: 502, the answer failed, the request pending
	// and taking another.
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "no such request"}
	})
	body := `{"answers":[{"questionId":"q1","choices":["Red"]},{"questionId":"q2","choices":["go"]}]}`
	rec := f.request(t, http.MethodPost, path+"/answer", body)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusBadGateway || problem.Code != api.CodeInputAnswerFailed || !strings.Contains(problem.Detail, "no such request") {
		t.Errorf("rejected: %d %+v", rec.Code, problem)
	}
	got := decodeInputRequest(t, f.request(t, http.MethodGet, path, ""))
	if got.Status != api.InputRequestPending || got.Answer == nil || got.Answer.State != api.InputAnswerFailed {
		t.Errorf("after rejection = %+v (answer %+v)", got, got.Answer)
	}
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{} })
	if rec = f.request(t, http.MethodPost, path+"/answer", body); rec.Code != http.StatusAccepted {
		t.Errorf("another answer after the failure: %d %s", rec.Code, rec.Body)
	}
	// Not connected: the answer already delivered is recovered — a retry
	// waits through a reconnect — while anything that would need sending
	// is refused before it is recorded.
	f.t3Server.DropConns()
	waitUntil(t, "the drop", func() bool { return f.t3.Connection().State != api.IntegrationConnected })
	if rec = f.request(t, http.MethodPost, path+"/answer", body); rec.Code != http.StatusAccepted || decodeInputRequest(t, rec).Answer.State != api.InputAnswerSent {
		t.Errorf("recovery while not connected: %d %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodPost, path+"/answer", `{"answers":[{"questionId":"q1","choices":["Blue"]},{"questionId":"q2","choices":["make"]}]}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusServiceUnavailable || problem.Code != api.CodeIntegrationNotConnected {
		t.Errorf("not connected: %d %+v", rec.Code, problem)
	}
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/stop", "{}")
	if problem := decodeProblem(t, rec); rec.Code != http.StatusServiceUnavailable || problem.Code != api.CodeIntegrationNotConnected {
		t.Errorf("stop while not connected: %d %+v", rec.Code, problem)
	}
}

// The stop path over the wire (ATC-308): a stop on running work returns
// 202 stopping with the thread refusing messages, answers, and decisions
// meanwhile — each by name — a second stop is the same operation, T3
// received one thread.session.stop, and T3's report of the session
// stopped at that instant confirms it: stopped, the turn interrupted,
// the request closed, and work accepted again. An idle thread's stop
// finishes at once with nothing sent.
func TestThreadStopOverTheWire(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.asking(t, 2)
	request := thread.InputRequests[0]
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)

	rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/stop", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("stop: got %d; body %s", rec.Code, rec.Body)
	}
	stop := decodeStop(t, rec)
	if stop.State != api.StopStopping || stop.Delivery != api.MessageAccepted || !strings.HasPrefix(stop.ID, "stop-") {
		t.Fatalf("stop = %+v", stop)
	}
	commands := f.t3Server.Commands()
	if len(commands) != 1 || commands[0]["type"] != "thread.session.stop" || commands[0]["threadId"] != "t1" {
		t.Errorf("commands = %v", commands)
	}
	if got := changes(sub); len(got) == 0 || got[0] != "thread.updated "+thread.ID {
		t.Errorf("events on stop = %v", got)
	}
	if after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")); after.Stop == nil || after.Stop.ID != stop.ID {
		t.Errorf("thread does not show the stop: %+v", after.Stop)
	}
	if rec = f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/stops/"+stop.ID, ""); rec.Code != http.StatusOK || decodeStop(t, rec).ID != stop.ID {
		t.Errorf("get stop: %d %s", rec.Code, rec.Body)
	}
	if rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/stop", "{}"); rec.Code != http.StatusAccepted || decodeStop(t, rec).ID != stop.ID || len(f.t3Server.Commands()) != 1 {
		t.Errorf("second stop: %d %s, %d commands", rec.Code, rec.Body, len(f.t3Server.Commands()))
	}
	for name, c := range map[string]struct{ path, body string }{
		"message":  {"/v1/threads/" + thread.ID + "/messages", `{"text":"more"}`},
		"answer":   {"/v1/threads/" + thread.ID + "/input-requests/" + request.ID + "/answer", `{"answers":[{"questionId":"q1","choices":["Red"]},{"questionId":"q2","choices":["go"]}]}`},
		"decision": {"/v1/threads/" + thread.ID + "/approvals/aprv-nope/decide", `{"decision":"approve"}`},
	} {
		rec := f.request(t, http.MethodPost, c.path, c.body)
		problem := decodeProblem(t, rec)
		if name == "decision" {
			// An unknown request is unknown before the restriction is
			// consulted; the restriction shows on a known one below.
			if rec.Code != http.StatusNotFound {
				t.Errorf("%s while stopping: %d %+v", name, rec.Code, problem)
			}
			continue
		}
		if rec.Code != http.StatusConflict || problem.Code != api.CodeThreadStopping || !strings.Contains(problem.Detail, stop.ID) {
			t.Errorf("%s while stopping: %d %+v", name, rec.Code, problem)
		}
	}
	if len(f.t3Server.Commands()) != 1 {
		t.Errorf("refused work reached T3: %v", f.t3Server.Commands())
	}
	// T3 processed the stop: session stopped at the command's createdAt,
	// the turn interrupted, the question still presented (stale).
	f.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt(commands[0]["createdAt"].(string)),
		t3codetest.LatestTurn("pt-1", "interrupted", "2026-09-01T00:00:03Z", commands[0]["createdAt"].(string)), t3codetest.Pending(false, true))))
	var confirmed api.ThreadStop
	waitUntil(t, "the stop confirmed", func() bool {
		confirmed = decodeStop(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/stops/"+stop.ID, ""))
		return confirmed.State != api.StopStopping
	})
	after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
	if confirmed.State != api.StopStopped || confirmed.ResolvedAt == nil || after.Stop != nil || after.LatestTurn.State != api.TurnInterrupted || len(after.InputRequests) != 0 {
		t.Errorf("confirmed = %+v; thread %+v", confirmed, after)
	}
	closedRequest := decodeInputRequest(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/input-requests/"+request.ID, ""))
	if closedRequest.Status != api.InputRequestResolved || closedRequest.Resolution != api.InputResolvedByStop {
		t.Errorf("request after the stop = %+v", closedRequest)
	}
	// The conversation continues.
	if rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"carry on"}`); rec.Code != http.StatusAccepted {
		t.Errorf("message after the stop: %d %s", rec.Code, rec.Body)
	}
	// Idle: finished at once, no command.
	idle := f.t3Thread(t, 4, "t2")
	sent := len(f.t3Server.Commands())
	rec = f.request(t, http.MethodPost, "/v1/threads/"+idle.ID+"/stop", `{"key":"idle-1"}`)
	finished := decodeStop(t, rec)
	if rec.Code != http.StatusAccepted || finished.State != api.StopFinished || finished.Key != "idle-1" || len(f.t3Server.Commands()) != sent {
		t.Errorf("stop on idle: %d %+v, %d commands", rec.Code, finished, len(f.t3Server.Commands())-sent)
	}
	// The same key returns the same stop, resolved; a new key on a thread
	// at rest is a new one.
	if rec = f.request(t, http.MethodPost, "/v1/threads/"+idle.ID+"/stop", `{"key":"idle-1"}`); rec.Code != http.StatusAccepted || decodeStop(t, rec).ID != finished.ID {
		t.Errorf("stop by the same key: %d %s", rec.Code, rec.Body)
	}
	if rec = f.request(t, http.MethodPost, "/v1/threads/"+idle.ID+"/stop", `{"key":"idle-2"}`); rec.Code != http.StatusAccepted || decodeStop(t, rec).ID == finished.ID {
		t.Errorf("stop by a new key: %d %s", rec.Code, rec.Body)
	}
	// Refusals by name.
	if rec = f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/stops/stop-nope", ""); rec.Code != http.StatusNotFound || decodeProblem(t, rec).Code != api.CodeStopNotFound {
		t.Errorf("get unknown stop: %d %s", rec.Code, rec.Body)
	}
	terminal := f.createRunningTerminal(t)
	claude := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)
	if rec = f.request(t, http.MethodPost, "/v1/threads/"+claude+"/stop", "{}"); rec.Code != http.StatusBadRequest || decodeProblem(t, rec).Code != api.CodeThreadStopUnsupported {
		t.Errorf("stop on a claude thread: %d %s", rec.Code, rec.Body)
	}
	if rec = f.request(t, http.MethodPost, "/v1/threads/thrd-nope/stop", "{}"); rec.Code != http.StatusNotFound {
		t.Errorf("stop on an unknown thread: %d", rec.Code)
	}
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{Reject: "cannot stop"} })
	f.t3Server.Push(t3codetest.Upserted(5, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-2", "running", "2026-09-01T00:00:05Z", nil))))
	waitUntil(t, "the new turn", func() bool {
		after = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
		return after.LatestTurn != nil && after.LatestTurn.State == api.TurnRunning
	})
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/stop", "{}")
	if problem := decodeProblem(t, rec); rec.Code != http.StatusBadGateway || problem.Code != api.CodeThreadStopFailed || !strings.Contains(problem.Detail, "cannot stop") {
		t.Errorf("rejected stop: %d %+v", rec.Code, problem)
	}
	if after = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")); after.Stop != nil {
		t.Errorf("a rejected stop stayed on the thread: %+v", after.Stop)
	}
}

// A message still in flight when the stop is accepted (ATC-308): the
// stop waits behind the message's dispatch, so T3 receives the message
// before the stop and the submitted turn is inside the stop's scope —
// withdrawn on confirmation and named by the stop, its key replaying
// the delivered message whose turn never started.
func TestThreadStopOrdersAfterInFlightMessage(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.t3Thread(t, 2, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))
	// T3 holds the message's command until released.
	release := make(chan struct{})
	var mu sync.Mutex
	var order []string
	f.t3Server.SetDispatch(func(command map[string]any) t3codetest.DispatchReply {
		kind, _ := command["type"].(string)
		if kind == "thread.turn.start" {
			<-release
		}
		mu.Lock()
		order = append(order, kind)
		mu.Unlock()
		return t3codetest.DispatchReply{}
	})
	messageDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		messageDone <- f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"then this","key":"k1"}`)
	}()
	waitUntil(t, "the message to reach T3", func() bool { return len(f.t3Server.Commands()) == 1 })
	stopDone := make(chan *httptest.ResponseRecorder, 1)
	go func() { stopDone <- f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/stop", "{}") }()
	// The stop waits: nothing more reaches T3 while the message is held.
	time.Sleep(50 * time.Millisecond)
	if len(f.t3Server.Commands()) != 1 {
		t.Fatalf("the stop overtook the message: %v", f.t3Server.Commands())
	}
	select {
	case rec := <-stopDone:
		t.Fatalf("the stop returned before the message was delivered: %d %s", rec.Code, rec.Body)
	default:
	}
	close(release)
	message := decodeMessage(t, <-messageDone)
	stop := decodeStop(t, <-stopDone)
	mu.Lock()
	got := append([]string(nil), order...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "thread.turn.start" || got[1] != "thread.session.stop" || stop.State != api.StopStopping || message.Delivery != api.MessageAccepted {
		t.Fatalf("order = %v; stop %+v; message %+v", got, stop, message)
	}
	commands := f.t3Server.Commands()
	f.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt(commands[1]["createdAt"].(string)),
		t3codetest.LatestTurn("pt-1", "interrupted", "2026-09-01T00:00:03Z", commands[1]["createdAt"].(string)))))
	var after api.Thread
	waitUntil(t, "the stop confirmed", func() bool {
		after = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
		return after.Stop == nil
	})
	confirmed := decodeStop(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/stops/"+stop.ID, ""))
	if confirmed.State != api.StopStopped || !strings.Contains(confirmed.Detail, message.TurnID) || after.PendingTurn != nil || after.LatestTurn.ID == message.TurnID || after.LatestTurn.State != api.TurnInterrupted {
		t.Errorf("confirmed = %+v; thread turns latest %+v pending %+v", confirmed, after.LatestTurn, after.PendingTurn)
	}
	// The message was delivered, so its key replays it; the turn it
	// directed is over.
	if rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"then this","key":"k1"}`); rec.Code != http.StatusAccepted || decodeMessage(t, rec).TurnID != message.TurnID {
		t.Errorf("replay after the stop: %d %s", rec.Code, rec.Body)
	}
}

// A message whose delivery was uncertain when the stop covered its
// turn is withdrawn on confirmation: its key replays the withdrawal,
// never a delivery into stopped work.
func TestThreadStopWithdrawsUncertainMessage(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.t3Thread(t, 2, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))
	f.t3Server.SetDispatch(func(command map[string]any) t3codetest.DispatchReply {
		if command["type"] == "thread.turn.start" {
			select {}
		}
		return t3codetest.DispatchReply{}
	})
	messageDone := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		messageDone <- f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"then this","key":"k1"}`)
	}()
	waitUntil(t, "the message to reach T3", func() bool { return len(f.t3Server.Commands()) == 1 })
	// The reconnect's snapshot must keep reporting the thread, or T3
	// dropping it would be the story instead.
	f.t3Server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(2, []any{t3codetest.ProjectItem("p1", "T3", f.projectDir)},
			[]any{t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))}), t3codetest.SynchronizedItem()}
	})
	f.t3Server.DropConns()
	message := decodeMessage(t, <-messageDone)
	if message.Delivery != api.MessageUncertain {
		t.Fatalf("message = %+v", message)
	}
	waitUntil(t, "the drop", func() bool { return f.t3.Connection().State != api.IntegrationConnected })
	waitUntil(t, "the reconnect", func() bool { return f.t3.Connection().State == api.IntegrationConnected })
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{} })
	rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/stop", "{}")
	if rec.Code != http.StatusAccepted {
		t.Fatalf("stop: %d %s", rec.Code, rec.Body)
	}
	stop := decodeStop(t, rec)
	commands := f.t3Server.Commands()
	created := commands[len(commands)-1]["createdAt"].(string)
	f.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt(created),
		t3codetest.LatestTurn("pt-1", "interrupted", "2026-09-01T00:00:03Z", created))))
	waitUntil(t, "the stop confirmed", func() bool {
		return decodeStop(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID+"/stops/"+stop.ID, "")).State == api.StopStopped
	})
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"then this","key":"k1"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusConflict || problem.Code != api.CodeThreadMessageWithdrawn || !strings.Contains(problem.Detail, stop.ID) {
		t.Errorf("replay of a withdrawn message: %d %+v", rec.Code, problem)
	}
	if sent := len(f.t3Server.Commands()); sent != len(commands) {
		t.Errorf("the replay reached T3: %v", f.t3Server.Commands()[len(commands):])
	}
}
