package posta

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ASR runs locally; bound its memory/CPU use across simultaneous catalog work.
var publicVideoASRSlots = make(chan struct{}, 1)

type whisperSegment struct {
	Offsets struct{ From, To int64 } `json:"offsets"`
	Text    string                   `json:"text"`
	Tokens  []struct {
		ID          int     `json:"id"`
		Probability float64 `json:"p"`
	} `json:"tokens"`
}

// Refuse incomplete/low-confidence speech rather than treating a partial
// transcript as the full audio. Gaps/quiet intros can conservatively abstain.
func completeWhisperTranscript(raw []byte, duration float64) (string, error) {
	var result struct {
		Transcription []whisperSegment `json:"transcription"`
	}
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || json.Unmarshal(raw, &result) != nil || len(result.Transcription) == 0 {
		return "", errors.New("missing transcript")
	}
	last := int64(0)
	var parts []string
	var sum float64
	var tokens, low int
	for i, s := range result.Transcription {
		if s.Offsets.From < 0 || s.Offsets.From >= int64(duration*1000) || s.Offsets.To <= s.Offsets.From || s.Offsets.To > int64(duration*1000)+3000 || (i == 0 && s.Offsets.From > 3000) || (i > 0 && (s.Offsets.From < last-1000 || s.Offsets.From-last > 5000)) {
			return "", errors.New("incomplete transcript coverage")
		}
		// Whisper rounds final timestamps beyond the file end. Do not treat a
		// short timestamp overrun as extra source audio or lose the final segment.
		last = min(s.Offsets.To, int64(duration*1000))
		if t := strings.TrimSpace(s.Text); t != "" {
			parts = append(parts, t)
		}
		for _, tok := range s.Tokens {
			if tok.ID < 0 || tok.ID >= 50257 {
				continue
			}
			if math.IsNaN(tok.Probability) || tok.Probability < 0 || tok.Probability > 1 {
				return "", errors.New("invalid confidence")
			}
			tokens++
			sum += tok.Probability
			if tok.Probability < .3 {
				low++
			}
		}
	}
	if last < int64(duration*1000)-3000 || len(parts) == 0 || tokens == 0 || sum/float64(tokens) < .65 || float64(low)/float64(tokens) > .2 {
		return "", errors.New("incomplete or uncertain transcript")
	}
	transcript := strings.Join(parts, " ")
	if len(transcript) > 40000 {
		return "", errors.New("transcript too large")
	}
	return transcript, nil
}

func (u *U) transcribePublicVideo(path, mime string, duration float64) ([]byte, string, error) {
	bin := filepath.Join(u.Cfg.Root, "data/local-asr-build/whisper.cpp-1.5.5/main")
	model := filepath.Join(u.Cfg.Root, "data/local-asr-build/ggml-small-q5_1.bin")
	if _, err := os.Stat(bin); err != nil {
		return nil, "", err
	}
	if _, err := os.Stat(model); err != nil {
		return nil, "", err
	}
	select {
	case publicVideoASRSlots <- struct{}{}:
		defer func() { <-publicVideoASRSlots }()
	case <-u.Ctx.Done():
		return nil, "", u.Ctx.Err()
	}
	dir, err := os.MkdirTemp("", "posta-local-asr-*")
	if err != nil {
		return nil, "", err
	}
	defer os.RemoveAll(dir)
	ffmpeg := u.Cfg.FFmpeg
	if ffmpeg == "" {
		ffmpeg = "ffmpeg"
	}
	ffprobe := u.Cfg.FFprobe
	if ffprobe == "" {
		ffprobe = "ffprobe"
	}
	wav := filepath.Join(dir, "full.wav")
	ctx, cancel := context.WithTimeout(u.Ctx, 90*time.Second)
	defer cancel()
	if !acquireFFmpeg(ctx) {
		return nil, "", ctx.Err()
	}
	err = exec.CommandContext(ctx, ffmpeg, "-v", "error", "-xerror", "-i", path, "-map", "0:a:0", "-vn", "-ac", "1", "-ar", "16000", "-c:a", "pcm_s16le", "-y", wav).Run()
	releaseFFmpeg()
	if err != nil {
		return nil, "", err
	}
	b, err := exec.CommandContext(ctx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", wav).Output()
	if err != nil {
		return nil, "", err
	}
	audioDuration, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil || math.IsNaN(audioDuration) || math.IsInf(audioDuration, 0) || math.Abs(audioDuration-duration) > 1 {
		return nil, "", errors.New("audio duration mismatch")
	}
	prefix := filepath.Join(dir, "transcript")
	asrCtx, asrCancel := context.WithTimeout(u.Ctx, 180*time.Second)
	defer asrCancel()
	err = exec.CommandContext(asrCtx, bin, "-m", model, "-f", wav, "-l", "auto", "-t", "4", "-ojf", "-of", prefix).Run()
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(prefix + ".json")
	if err != nil || len(raw) > 4<<20 {
		return nil, "", errors.New("transcript unavailable")
	}
	transcript, err := completeWhisperTranscript(raw, audioDuration)
	if err != nil {
		return nil, "", err
	}
	ext := ".mp4"
	if mime == "video/webm" {
		ext = ".webm"
	} else if mime == "video/mpeg" {
		ext = ".mpeg"
	}
	visual := filepath.Join(dir, "visual"+ext)
	visualCtx, visualCancel := context.WithTimeout(u.Ctx, 90*time.Second)
	defer visualCancel()
	if !acquireFFmpeg(visualCtx) {
		return nil, "", visualCtx.Err()
	}
	err = exec.CommandContext(visualCtx, ffmpeg, "-v", "error", "-xerror", "-i", path, "-map", "0:v:0", "-c:v", "copy", "-an", "-y", visual).Run()
	releaseFFmpeg()
	if err != nil {
		return nil, "", err
	}
	b, err = exec.CommandContext(visualCtx, ffprobe, "-v", "error", "-show_entries", "format=duration", "-of", "csv=p=0", visual).Output()
	if err != nil {
		return nil, "", err
	}
	visualDuration, err := strconv.ParseFloat(strings.TrimSpace(string(b)), 64)
	if err != nil || math.IsNaN(visualDuration) || math.IsInf(visualDuration, 0) || math.Abs(visualDuration-duration) > 1 {
		return nil, "", errors.New("visual duration mismatch")
	}
	data, err := os.ReadFile(visual)
	if err != nil || len(data) == 0 || len(data) > publicClassificationVideoMaxBytes {
		return nil, "", errors.New("visual data unavailable")
	}
	return data, transcript, nil
}
