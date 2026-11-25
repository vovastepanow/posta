package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestAITextNeedsRepair(t *testing.T) {
	clipped := jx.M{"description": strings.Repeat("а", 120)}
	if !AITextNeedsRepair(clipped) {
		t.Fatal("legacy truncated description not detected")
	}
	if AITextNeedsRepair(jx.M{"description": strings.Repeat("а", 119) + "."}) ||
		AITextNeedsRepair(jx.M{"description": strings.Repeat("а", 120), "ai_text_repaired": true}) ||
		AITextNeedsRepair(jx.M{"description": strings.Repeat("а", 120), "own": true}) {
		t.Fatal("complete or excluded record incorrectly selected")
	}
	if !AITextNeedsRepair(jx.M{"why_relevant": strings.Repeat("а", 79)}) {
		t.Fatal("legacy truncated explanation not detected")
	}
}
