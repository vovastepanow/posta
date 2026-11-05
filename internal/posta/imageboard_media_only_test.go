package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestImageboardAnalysisIgnoresText(t *testing.T) {
	for _, source := range []string{"dvach:b", "4chan:b:123"} {
		item := jx.M{"source_key": source, "text": "текст поста", "thread_title": "заголовок", "thread_op": "шапка"}
		if got := PostCaptionForAI(item); got != "" {
			t.Errorf("%s: unexpected caption %q", source, got)
		}
		if got := queryLex(item, QueryPlan{Must: []string{"заголовок"}}); got != 0 {
			t.Errorf("%s: thread text affected query ranking: %v", source, got)
		}
		if got := AnalysisContextBlock(item); got != "" {
			t.Errorf("%s: unexpected context %q", source, got)
		}
	}
	if got := PostCaptionForAI(jx.M{"source_key": "other:1", "text": "подпись"}); got != "подпись" {
		t.Errorf("other source caption = %q", got)
	}
}

func TestImageboardCachedCandidateIsMediaOnly(t *testing.T) {
	item := jx.M{"source_key": "4chan:b:123", "image": "https://example.com/a.jpg", "text": "реклама", "title": "заголовок", "thread_title": "тред", "thread_op": "шапка", "thread_trust": true, "captions": []any{"подпись"}}
	items := ExpandPostImages([]jx.M{item})
	if len(items) != 1 {
		t.Fatalf("got %d candidates", len(items))
	}
	for _, key := range []string{"text", "title", "thread_title", "thread_op", "thread_trust"} {
		if _, ok := items[0][key]; ok {
			t.Errorf("candidate retained %s", key)
		}
	}
	if got := jx.Str(item, "text"); got != "реклама" {
		t.Errorf("original was changed: %q", got)
	}
	if AnalysisCacheKey(item, "мемы") == AnalysisCacheKey(jx.M{"source_key": "other:1", "image": item["image"]}, "мемы") {
		t.Error("imageboard analysis must not reuse old analysis cache")
	}
}
