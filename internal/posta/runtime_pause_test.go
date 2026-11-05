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
