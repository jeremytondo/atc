package linear

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/t3code"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
	"github.com/jeremytondo/atc/internal/webhooks"
)

// idleDriver is the terminals domain's driver seam for a fixture that
// never runs a terminal.
type idleDriver struct{}

func (idleDriver) Inventory(context.Context) ([]terminals.Session, error) { return nil, nil }
func (idleDriver) Create(context.Context, string, terminals.CreateSpec) error {
	return nil
}
func (idleDriver) Kill(context.Context, string) error { return nil }

// The user-visible flow over the real seams: the application coordinator
// starts the Thread in a fake T3 Code environment through the T3 Code
// Integration, T3's shell reports the turn, its response is recovered
// from T3's detail snapshot, and Linear receives the acknowledgement, the
// T3 links, and the answer; then a follow-up message becomes the next
// turn.start on the same T3 thread, the agent's question is presented
// with its choices, a choice and a reply in words each become a
// user-input.respond in T3's shape, and the next turn's answer comes
// back (ATC-309).
func TestMentionThroughTheCoordinatorAndT3Code(t *testing.T) {
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := events.NewHubAt(256, 1)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := canonical(t, t.TempDir())
	terminalService := terminals.NewService(terminals.Options{Repository: db.Terminals(), Driver: idleDriver{}, Spaces: db.Spaces(), HomeDir: root, MarkerDir: t.TempDir(), Hub: hub, Logger: logger})
	if err := terminalService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	projectService := projects.NewService(projects.Options{Repository: db.Projects(), Hub: hub})
	threadService := threads.NewService(threads.Options{Repository: db.Threads(), Terminals: terminalService, Projects: db.Projects(), Hub: hub, Logger: logger})
	if err := threadService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	project, err := projectService.Create(context.Background(), api.ProjectCreateParams{Directory: root, Name: "atc"})
	if err != nil {
		t.Fatal(err)
	}
	t3Server := t3codetest.NewServer(t)
	t3Home := t.TempDir()
	t3Service := t3code.New(t3code.Options{
		Home: t3Home, SessionPath: filepath.Join(t.TempDir(), "t3code-session.json"), Threads: threadService, Hub: hub, Logger: logger,
		RunCLI: t3codetest.NewCLI(t3Server).Run, ProcessAlive: func(int) bool { return true },
	})
	threadService.SetLinker(t3code.ID, t3Service.Links)
	catalog, err := integrations.NewService(integrations.Options{Integrations: []integrations.Integration{t3code.Integration(t3Service)}})
	if err != nil {
		t.Fatal(err)
	}
	coordinator := application.New(application.Options{Terminals: terminalService, Threads: threadService, Projects: projectService, Integrations: catalog, Logger: logger})
	t3codetest.Connect(t, t3Server, t3Home, root, t3Service.Run, t3Service.Connection)

	linearAPI := newFakeLinear(t)
	setupPath := filepath.Join(t.TempDir(), "linear.json")
	f := &fixture{t: t, store: db, setupPath: setupPath}
	f.writeSetup(Setup{OrganizationID: testOrg, ClientID: testClient, ClientSecret: "secret-1", WebhookSigningSecret: testSecret, AccessToken: "token-0", RefreshToken: "refresh-0", ProjectID: project.ID})
	service := New(Options{SetupPath: setupPath, Repository: db.Linear(), Coordinator: coordinator, Threads: threadService, Hub: hub, APIURL: linearAPI.srv.URL, Logger: logger})
	service.sendPoll, service.sessionPoll, service.probeRetry, service.retryBase = testPollFast, testPollFast, testPollFast, testPollFast
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	if err := service.Process(context.Background(), accepted(createdEvent("sess-1", time.Now()))); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "one T3 command", func() bool { return len(t3Server.Commands()) == 1 })
	command := t3Server.Commands()[0]
	t3ThreadID, _ := command["threadId"].(string)
	selection, _ := command["modelSelection"].(map[string]any)
	if selection["instanceId"] != "codex" || selection["model"] != "gpt-5.6-sol" {
		t.Errorf("model selection = %v", selection)
	}
	if options, _ := selection["options"].([]any); len(options) != 1 || options[0].(map[string]any)["id"] != "reasoningEffort" || options[0].(map[string]any)["value"] != "high" {
		t.Errorf("options = %v", selection["options"])
	}
	if text, _ := command["message"].(map[string]any)["text"].(string); !strings.Contains(text, "mention of @atc in Linear issue ATC-302") || !strings.Contains(text, "<comment>@atc what does the webhook receiver do?</comment>") {
		t.Errorf("prompt = %q", text)
	}
	var session store.LinearSession
	waitFor(t, "session bound", func() bool {
		session, _ = db.Linear().GetSession(context.Background(), "sess-1")
		return session.State == stateBound
	})
	start, _ := db.Linear().Submissions(context.Background(), "sess-1", false)
	if len(start) != 1 || start[0].Kind != kindStart || start[0].TurnID == "" {
		t.Fatalf("start submission = %+v", start)
	}
	waitFor(t, "links", func() bool { return len(linearAPI.linksOf("sess-1")) == 2 })
	if links := linearAPI.linksOf("sess-1"); links[0].URL != t3Server.Origin()+"/env-1/"+t3ThreadID || links[1].URL != "t3code://threads/env-1/"+t3ThreadID {
		t.Errorf("links = %+v", links)
	}

	// T3 reports the thread and its turn running, then completed, with the
	// final message in the detail snapshot.
	thread := t3codetest.ThreadItem(t3ThreadID, "p1", "T", t3codetest.Model("codex", "gpt-5.6-sol"), t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("t3-turn-1", "running", "2026-09-05T12:00:01Z", nil))
	t3Server.Push(t3codetest.Upserted(2, thread))
	waitFor(t, "turn bound", func() bool {
		got, err := threadService.Get(session.ThreadID)
		return err == nil && got.LatestTurn != nil && got.LatestTurn.ID == start[0].TurnID && got.Status == api.ThreadWorking
	})
	completed := t3codetest.ThreadItem(t3ThreadID, "p1", "T", t3codetest.Model("codex", "gpt-5.6-sol"), t3codetest.WithSession("idle", "codex"),
		t3codetest.LatestTurn("t3-turn-1", "completed", "2026-09-05T12:00:01Z", "2026-09-05T12:00:09Z"), t3codetest.AssistantMessage("msg-1"))
	t3Server.SetThreadDetail(t3ThreadID, t3codetest.ThreadDetailItem(completed, t3codetest.MessageItem("msg-1", "assistant", "It relays public traffic to Core.", "t3-turn-1", false)))
	t3Server.Push(t3codetest.Upserted(3, completed))

	var acts []recordedActivity
	waitFor(t, "the answer in Linear", func() bool {
		acts = linearAPI.activitiesOf("sess-1")
		return len(acts) >= 3
	})
	if acts[2].Type != contentResponse || acts[2].Body != "It relays public traffic to Core." {
		t.Errorf("result = %+v", acts[2])
	}
	if got, _ := db.Linear().GetSession(context.Background(), "sess-1"); got.State != stateBound {
		t.Errorf("session = %+v", got)
	}
	if len(t3Server.Commands()) != 1 {
		t.Errorf("T3 received %d commands, want 1", len(t3Server.Commands()))
	}

	// The follow-up continues the same T3 thread: one thread.turn.start
	// under the thread's own modes, with the text as written and
	// identities derived from the activity's.
	prompted := func(deliveryID, text string) {
		t.Helper()
		if err := service.Process(context.Background(), webhooks.Accepted{ID: "whk-" + deliveryID, DeliveryID: deliveryID, Payload: promptedEvent("sess-1", time.Now(), text, "", withActivityID("act-"+deliveryID))}); err != nil {
			t.Fatal(err)
		}
	}
	prompted("dlv-2", "and the sandbox?")
	waitFor(t, "the message in T3", func() bool { return len(t3Server.Commands()) == 2 })
	followUp := t3Server.Commands()[1]
	if followUp["type"] != "thread.turn.start" || followUp["threadId"] != t3ThreadID || followUp["message"].(map[string]any)["text"] != "and the sandbox?" || followUp["modelSelection"] != nil {
		t.Errorf("follow-up command = %v", followUp)
	}
	waitFor(t, "the message acknowledged", func() bool { return len(linearAPI.activitiesOf("sess-1")) >= 4 })
	// T3 runs the second turn, asks a question; the question is
	// presented with the choices, and a prompt naming a choice's value
	// answers that question alone through thread.user-input.respond.
	asking := t3codetest.ThreadItem(t3ThreadID, "p1", "T", t3codetest.Model("codex", "gpt-5.6-sol"), t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("t3-turn-2", "running", "2026-09-05T12:01:00Z", nil), t3codetest.Pending(false, true))
	requested := t3codetest.UserInputRequested("a1", "req-1",
		t3codetest.Question("scope", "Scope", "Which sandbox?", t3codetest.QuestionOption("Receiver", "the webhook receiver"), t3codetest.QuestionOption("Server", "the API server")))
	t3Server.SetThreadDetail(t3ThreadID, t3codetest.WithActivities(t3codetest.ThreadDetailItem(asking), requested))
	t3Server.Push(t3codetest.Upserted(4, asking))
	waitFor(t, "the question in Linear", func() bool {
		acts = linearAPI.activitiesOf("sess-1")
		return len(acts) >= 5 && acts[4].Type == contentElicitation
	})
	ask := acts[4]
	if ask.Signal != signalSelect || len(ask.Options) != 2 || ask.Options[0].Label != "Receiver" || !strings.Contains(ask.Body, "Which sandbox?") {
		t.Errorf("ask = %+v", ask)
	}
	prompted("dlv-3", ask.Options[1].Value)
	waitFor(t, "the answer in T3", func() bool { return len(t3Server.Commands()) == 3 })
	answer := t3Server.Commands()[2]
	if answer["type"] != "thread.user-input.respond" || answer["requestId"] != "req-1" || answer["answers"].(map[string]any)["scope"] != "Server" {
		t.Errorf("answer command = %v", answer)
	}
	// A reply in words, while a second question is open, goes verbatim
	// as the first question's answer.
	resolved := t3codetest.ActivityAt(t3codetest.UserInputResolved("b1", "req-1", map[string]any{"scope": "Server"}), answer["createdAt"].(string))
	second := t3codetest.UserInputRequested("a2", "req-2",
		t3codetest.Question("depth", "Depth", "How deep?", t3codetest.QuestionOption("Brief", ""), t3codetest.QuestionOption("Thorough", "")),
		t3codetest.Question("tests", "Tests", "Run the tests?", t3codetest.QuestionOption("Yes", ""), t3codetest.QuestionOption("No", "")))
	t3Server.SetThreadDetail(t3ThreadID, t3codetest.WithActivities(t3codetest.ThreadDetailItem(asking), requested, resolved, second))
	t3Server.Push(t3codetest.Upserted(5, asking))
	waitFor(t, "the second question in Linear", func() bool {
		acts = linearAPI.activitiesOf("sess-1")
		return len(acts) >= 8
	})
	prompted("dlv-4", "Brief is fine, and skip the tests for now.")
	waitFor(t, "the reply in T3", func() bool { return len(t3Server.Commands()) == 4 })
	reply := t3Server.Commands()[3]
	if got := reply["answers"].(map[string]any); reply["type"] != "thread.user-input.respond" || reply["requestId"] != "req-2" || got["depth"] != "Brief is fine, and skip the tests for now." || len(got) != 1 {
		t.Errorf("reply command = %v", reply)
	}
	// The turn ends with its response: posted against this turn.
	finished := t3codetest.ThreadItem(t3ThreadID, "p1", "T", t3codetest.Model("codex", "gpt-5.6-sol"), t3codetest.WithSession("idle", "codex"),
		t3codetest.LatestTurn("t3-turn-2", "completed", "2026-09-05T12:01:00Z", "2026-09-05T12:02:00Z"), t3codetest.AssistantMessage("msg-2"))
	t3Server.SetThreadDetail(t3ThreadID, t3codetest.WithActivities(t3codetest.ThreadDetailItem(finished, t3codetest.MessageItem("msg-2", "assistant", "The server sandbox denies the API port.", "t3-turn-2", false)),
		requested, resolved, second, t3codetest.ActivityAt(t3codetest.UserInputResolved("b2", "req-2", map[string]any{"depth": "Brief is fine, and skip the tests for now."}), reply["createdAt"].(string))))
	t3Server.Push(t3codetest.Upserted(6, finished))
	waitFor(t, "the second answer in Linear", func() bool {
		acts = linearAPI.activitiesOf("sess-1")
		return len(acts) > 0 && acts[len(acts)-1].Type == contentResponse && acts[len(acts)-1].Body == "The server sandbox denies the API port."
	})
	if len(t3Server.Commands()) != 4 {
		t.Errorf("T3 received %d commands, want 4", len(t3Server.Commands()))
	}
}
