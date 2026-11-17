package posta

import (
	"encoding/json"
	"testing"
)

func TestCompleteWhisperTranscriptRejectsMissingOrUncertainAudio(t *testing.T) {
	segment := func(from, to int, text string, p float64) map[string]any {
		return map[string]any{"offsets": map[string]any{"from": from, "to": to}, "text": text, "tokens": []any{map[string]any{"id": 123, "p": p}}}
	}
	tests := []struct {
		name     string
		segments []map[string]any
		want     bool
	}{
		{"complete", []map[string]any{segment(0, 5000, "полная", .9), segment(5000, 10000, "расшифровка", .9)}, true},
		{"missing end", []map[string]any{segment(0, 5000, "обрыв", .9)}, false},
		{"missing beginning", []map[string]any{segment(4000, 10000, "обрыв", .9)}, false},
		{"low confidence", []map[string]any{segment(0, 10000, "неразборчиво", .1)}, false},
		{"empty", nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, _ := json.Marshal(map[string]any{"transcription": tc.segments})
			out, err := completeWhisperTranscript(raw, 10)
			if (err == nil) != tc.want {
				t.Fatalf("text=%q err=%v", out, err)
			}
		})
	}
}
