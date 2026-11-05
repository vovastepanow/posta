package posta

import (
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/imgx"
)

func TestStoryboardFrameCount(t *testing.T) {
	for _, tc := range []struct {
		duration    float64
		limit, want int
	}{
		{1, 30, 2}, {14, 30, 28}, {15, 30, 30}, {15.1, 30, 16}, {20, 30, 20}, {60, 30, 30}, {60, 8, 8}, {0, 30, 4},
	} {
		if got := storyboardFrameCount(tc.duration, tc.limit); got != tc.want {
			t.Errorf("duration=%v limit=%d: got %d want %d", tc.duration, tc.limit, got, tc.want)
		}
	}
}

func TestStoryboardRealVideo(t *testing.T) {
	path := os.Getenv("POSTA_TEST_VIDEO")
	if path == "" {
		t.Skip("set POSTA_TEST_VIDEO to a real video")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip(err)
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip(err)
	}
	a := &App{Cfg: config.Server{FFmpeg: ffmpeg, FFprobe: ffprobe}}
	for _, duration := range []int{10, 15, 20, 60} {
		clip := filepath.Join(t.TempDir(), "clip.mp4")
		cmd := exec.Command(ffmpeg, "-v", "error", "-i", path, "-t", strconv.Itoa(duration), "-c", "copy", "-y", clip)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("clip: %v %s", err, out)
		}
		start := time.Now()
		board := a.videoStoryboard(clip, 30)
		if board == nil {
			t.Fatalf("%ds: no frames", duration)
		}
		want := storyboardFrameCount(board.Duration, 30)
		if len(board.Frames) != want {
			t.Fatalf("%ds duration %.3f: got %d frames, want %d", duration, board.Duration, len(board.Frames), want)
		}
		for i, frame := range board.Frames {
			if err := imgx.Validate(frame); err != nil {
				t.Fatalf("invalid frame %d: %v", i, err)
			}
			target := (float64(i) + .5) * board.Duration / float64(want)
			if math.Abs(board.Timestamps[i]-target) > .2 {
				t.Fatalf("frame %d at %.3f expected %.3f", i, board.Timestamps[i], target)
			}
		}
		t.Logf("clip=%ds duration=%.2fs frames=%d first=%.2fs last=%.2fs extraction=%s", duration, board.Duration, want, board.Timestamps[0], board.Timestamps[want-1], time.Since(start))
	}
}
