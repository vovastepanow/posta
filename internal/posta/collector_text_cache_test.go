package posta

import "testing"

func TestTextVerdictKeySeparatesScopeAndEvidence(t *testing.T) {
	key := textVerdictKey("", "full", "prompt", "text", "models")
	for _, other := range []string{
		textVerdictKey("owner:a", "full", "prompt", "text", "models"),
		textVerdictKey("", "caption", "prompt", "text", "models"),
		textVerdictKey("", "full", "new prompt", "text", "models"),
		textVerdictKey("", "full", "prompt", "other text", "models"),
		textVerdictKey("", "full", "prompt", "text", "other models"),
	} {
		if key == other {
			t.Fatal("different text verdict input reused cache")
		}
	}
}
