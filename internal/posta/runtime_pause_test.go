package posta

import (
	"context"
	"testing"
	"time"
)

func TestPauseAllParsersBlocksAllExistingSessions(t *testing.T) {
	a := &App{}
	a.parsers = newParserManager(a)
	active := a.parsers.user("active")
	a.parsers.user("idle")
	ctx, cancel := context.WithCancel(context.Background())
	active.running, active.cancel = true, cancel
	done := make(chan struct{})
	go func() {
		<-ctx.Done()
		active.mu.Lock()
		active.running = false
		active.mu.Unlock()
		close(done)
	}()
	if n := a.PauseAllParsers(); n != 1 {
		t.Fatalf("cancelled %d parsers, want 1", n)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("active parser was not cancelled")
	}
	if !a.ParserPaused("active") || !a.ParserPaused("idle") {
		t.Fatal("all existing sessions must be paused, including idle ones")
	}
	if a.ParserRunning("active") {
		t.Fatal("parser still marked running")
	}
}

func TestStopSourceDiscoveryDoesNotStopCollector(t *testing.T) {
	a := &App{}
	a.parsers = newParserManager(a)
	u := &U{App: a, ID: "user", Ctx: context.Background()}
	collectorCtx, collectorCancel := context.WithCancel(context.Background())
	defer collectorCancel()
	p := a.parsers.user(u.ID)
	p.running, p.cancel = true, collectorCancel
	discoveryCtx, discoveryCancel := context.WithCancel(context.Background())
	run := &discoveryRun{cancel: discoveryCancel, done: make(chan struct{})}
	a.parsers.discoveryRuns[TopicNorm("Тема")] = run
	go func() { <-discoveryCtx.Done(); close(run.done) }()
	stopped, stopping := u.StopSourceDiscovery("Тема")
	if !stopped || stopping || !a.parsers.discoveryPaused[TopicNorm("Тема")] {
		t.Fatal("discovery should be stopped and paused")
	}
	if collectorCtx.Err() != nil || !a.ParserRunning(u.ID) {
		t.Fatal("stopping discovery must not stop the collector")
	}
}

func TestPauseParserDoesNotStopDiscovery(t *testing.T) {
	a := &App{}
	a.parsers = newParserManager(a)
	u := &U{App: a, ID: "user", Ctx: context.Background()}
	discoveryCtx, discoveryCancel := context.WithCancel(context.Background())
	defer discoveryCancel()
	a.parsers.discoveryRuns["topic"] = &discoveryRun{cancel: discoveryCancel, done: make(chan struct{})}
	collectorCtx, collectorCancel := context.WithCancel(context.Background())
	p := a.parsers.user(u.ID)
	p.running, p.cancel = true, collectorCancel
	go func() { <-collectorCtx.Done(); p.mu.Lock(); p.running = false; p.mu.Unlock() }()
	if !u.PauseParser() || !a.ParserPaused(u.ID) {
		t.Fatal("collector should be stopped and paused")
	}
	if discoveryCtx.Err() != nil {
		t.Fatal("stopping the collector must not stop discovery")
	}
	u.ResumeParser()
	if a.ParserPaused(u.ID) {
		t.Fatal("manual start must clear the pause")
	}
}
