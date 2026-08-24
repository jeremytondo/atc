package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/core"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/fakes"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/httpapi"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/plugin"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:7428", "HTTP listen address")
	demo := flag.Bool("demo", false, "run the deterministic scenario and print its JSON result")
	flag.Parse()

	agent := fakes.NewAgent()
	workspace := fakes.NewWorkspace()
	service, err := core.New(agent, workspace)
	if err != nil {
		log.Fatal(err)
	}
	if err := service.RefreshAll(context.Background()); err != nil {
		log.Fatal(err)
	}
	if *demo {
		if err := runDemo(service, agent); err != nil {
			log.Fatal(err)
		}
		return
	}

	server := &http.Server{
		Addr: *listen, Handler: httpapi.New(service),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownContext)
	}()
	fmt.Printf("ATC orchestration prototype listening on http://%s\n", *listen)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

func runDemo(service *core.Service, agent *fakes.Agent) error {
	ctx := context.Background()
	created, err := service.Create(ctx, "fake-agent", plugin.CreateRequest{Kind: "agent_session", Title: "Prototype task"})
	if err != nil {
		return err
	}
	needsInput, err := agent.NeedsInput(created.Source.NativeID)
	if err != nil {
		return err
	}
	if _, err := service.Update("fake-agent", needsInput); err != nil {
		return err
	}
	if _, err := service.Act(ctx, created.ID, model.CapabilityRespond, plugin.ActionRequest{Text: "Continue"}); err != nil {
		return err
	}
	if _, err := service.Act(ctx, created.ID, model.CapabilityCancel, plugin.ActionRequest{}); err != nil {
		return err
	}
	observed := service.Resources("fake-workspace", "coding_session")[0]
	opened, err := service.Act(ctx, observed.ID, model.CapabilityOpenExternal, plugin.ActionRequest{})
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"plugins": service.Plugins(), "resources": service.Resources("", ""),
		"events": service.EventsAfter(0), "openExternal": opened.Link,
	})
}
