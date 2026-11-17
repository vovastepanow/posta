package posta

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posta/internal/config"
)

func TestAutopilotYtdlpCannotBypassRequestBudget(t *testing.T) {
	dir := t.TempDir()
	bin, marker := filepath.Join(dir, "fake-ytdlp"), filepath.Join(dir, "ran")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf ran > '"+marker+"'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: config.Server{YtDlp: bin, DataDir: dir, Root: dir}}
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	r := a.YtdlpRun(ctx, nil, time.Second, false, nil)
	if r.OK || !strings.Contains(r.Stderr, "budget unavailable") {
		t.Fatalf("leased subprocess accepted: %+v", r)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("leased operation ran executable: %v", err)
	}
	r = a.YtdlpRun(context.Background(), nil, time.Second, false, nil)
	if !r.OK {
		t.Fatalf("ordinary execution changed: %+v", r)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("ordinary operation did not run executable: %v", err)
	}
}
