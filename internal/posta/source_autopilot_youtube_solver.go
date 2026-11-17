package posta

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// These scripts are copied from the bundled yt-dlp executable; their bundled
// third-party licence notices are kept beside them.
//
//go:embed youtube_ejs/lib.min.js
var youtubeEJSLib string

//go:embed youtube_ejs/core.min.js
var youtubeEJSCore string

type youtubeChallenge struct {
	Type       string   `json:"type"`
	Challenges []string `json:"challenges"`
}

type boundedSolverOutput struct{ bytes.Buffer }

func (b *boundedSolverOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 64<<10 {
		return 0, errors.New("solver output too large")
	}
	return b.Buffer.Write(p)
}

// The solver receives the already fetched player and challenges on stdin.
// Deno has no network, filesystem, environment, subprocess or FFI access.
func (a *App) solveAutopilotYouTube(ctx context.Context, player []byte, requests []youtubeChallenge) ([]map[string]string, error) {
	if len(player) == 0 || len(player) > 5<<20 || len(requests) == 0 || len(requests) > 2 {
		return nil, errors.New("invalid YouTube challenge")
	}
	for _, r := range requests {
		if (r.Type != "sig" && r.Type != "n") || len(r.Challenges) != 1 || len(r.Challenges[0]) == 0 || len(r.Challenges[0]) > 2048 {
			return nil, errors.New("invalid YouTube challenge")
		}
	}
	input, err := json.Marshal(struct {
		Type     string             `json:"type"`
		Player   string             `json:"player"`
		Requests []youtubeChallenge `json:"requests"`
		Output   bool               `json:"output_preprocessed"`
	}{"player", string(player), requests, false})
	if err != nil {
		return nil, errors.New("invalid YouTube challenge")
	}
	deno := filepath.Join(a.Cfg.Root, "tools", "bin", "deno")
	if !fileExists(deno) {
		return nil, errors.New("YouTube challenge runtime unavailable")
	}
	dir, err := os.MkdirTemp("", "posta-youtube-solver-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	work, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(work, deno, "run", "--no-config", "--no-prompt", "--no-remote", "--no-npm", "--deny-net", "--deny-read", "--deny-write", "--deny-env", "--deny-run", "--deny-ffi", "-")
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir, "DENO_DIR=" + dir, "DENO_NO_UPDATE_CHECK=1"}
	cmd.Stdin = io.MultiReader(bytes.NewBufferString(youtubeEJSLib+"\nObject.assign(globalThis,lib);\n"+youtubeEJSCore+"\nconsole.log(JSON.stringify(jsc("), bytes.NewReader(input), bytes.NewBufferString(")));\n"))
	var stdout boundedSolverOutput
	cmd.Stdout, cmd.Stderr = &stdout, io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("YouTube challenge solver unavailable")
	}
	var output struct {
		Type      string `json:"type"`
		Responses []struct {
			Type string            `json:"type"`
			Data map[string]string `json:"data"`
		} `json:"responses"`
	}
	if json.Unmarshal(stdout.Bytes(), &output) != nil || output.Type != "result" || len(output.Responses) != len(requests) {
		return nil, errors.New("invalid YouTube challenge result")
	}
	results := make([]map[string]string, 0, len(requests))
	for i, response := range output.Responses {
		value := response.Data[requests[i].Challenges[0]]
		if response.Type != "result" || value == "" || len(value) > 4096 {
			return nil, errors.New("YouTube challenge unresolved")
		}
		results = append(results, response.Data)
	}
	return results, nil
}
