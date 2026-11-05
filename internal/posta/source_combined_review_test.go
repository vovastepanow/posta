package posta

import "testing"

func TestCombinedSourceReview(t *testing.T) {
	ev := parseCombinedSourceReview(`{"text_score":80,"image_score":60,"taste_score":75,"reason":"по теме"}`, true)
	if ev == nil || ev.Score != 60 || ev.Text == nil || *ev.Text != 80 || len(ev.Images) != 1 || ev.Images[0] != 60 || ev.Taste == nil || *ev.Taste != 75 {
		t.Fatalf("unexpected assessment: %+v", ev)
	}
	if parseCombinedSourceReview(`{"text_score":80,"image_score":60}`, true) != nil {
		t.Fatal("missing taste must use the original assessment")
	}
	ev = parseCombinedSourceReview(`{"text_score":80,"image_score":60}`, false)
	if ev == nil || ev.Taste != nil || !ev.TasteIncluded {
		t.Fatalf("unexpected result without history: %+v", ev)
	}
}
