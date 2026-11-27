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

func TestPauseParserAndResume(t *testing.T) {
	a := &App{}
	a.parsers = newParserManager(a)
	u := &U{App: a, ID: "user", Ctx: context.Background()}
	collectorCtx, collectorCancel := context.WithCancel(context.Background())
	p := a.parsers.user(u.ID)
	p.running, p.cancel = true, collectorCancel
	go func() { <-collectorCtx.Done(); p.mu.Lock(); p.running = false; p.mu.Unlock() }()
	if !u.PauseParser() || !a.ParserPaused(u.ID) {
		t.Fatal("collector should be stopped and paused")
	}
	u.ResumeParser()
	if a.ParserPaused(u.ID) {
		t.Fatal("manual start must clear the pause")
	}
}
