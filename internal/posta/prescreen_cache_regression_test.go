package posta

import "testing"

func TestPrescreenCacheBindsActualEvidence(t *testing.T) {
	key := prescreenEvidenceKey("topic", "", "caption", "prompt", []byte("image"))
	for _, changed := range []string{
		prescreenEvidenceKey("other", "", "caption", "prompt", []byte("image")),
		prescreenEvidenceKey("topic", "owner", "caption", "prompt", []byte("image")),
		prescreenEvidenceKey("topic", "", "edited", "prompt", []byte("image")),
		prescreenEvidenceKey("topic", "", "caption", "new prompt", []byte("image")),
		prescreenEvidenceKey("topic", "", "caption", "prompt", []byte("edited image")),
	} {
		if key == changed {
			t.Fatal("different evidence reused the prescreen verdict")
		}
	}
	if key != prescreenEvidenceKey("topic", "", "caption", "prompt", []byte("image")) {
		t.Fatal("same evidence lost its cache entry")
	}
}
