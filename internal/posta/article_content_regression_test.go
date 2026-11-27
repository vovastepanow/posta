package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestOsnovaPreservesLinksAndUnsupportedBlocks(t *testing.T) {
	blocks := osnovaBlocks([]jx.M{
		{"type": "header", "data": jx.M{"text": "<a href='https://example.org'>Heading</a>"}},
		{"type": "text", "data": jx.M{"text": "<p>#smm</p>"}},
		{"type": "quote", "data": jx.M{"text": "<a href=\"http://example.org/a\">Citation</a>"}},
		{"type": "media", "data": jx.M{"items": []jx.M{{"type": "video"}}}},
		{"type": "unknown"},
	}, "https://vc.ru/123456")
	if len(blocks) != 5 || len(jx.List(blocks[0], "links")) != 1 || jx.Str(blocks[1], "text") != "#smm" ||
		len(jx.List(blocks[2], "links")) != 1 || jx.Str(blocks[3], "type") != "unsupported" || jx.Str(blocks[4], "type") != "unsupported" {
		t.Fatalf("article blocks lost: %+v", blocks)
	}
	if len(osnovaLinks(`<a href="javascript:alert(1)">unsafe</a><a href="https://user:pass@example.org">private</a>`)) != 0 {
		t.Fatal("unsafe link retained")
	}
	if _, _, err := calendarStructuredArticle(jx.M{"blocks": blocks}, "", nil, false); err == nil {
		t.Fatal("incomplete article admitted to automatic publication")
	}
}
