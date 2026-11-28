package posta

import (
	"math"
	"testing"

	"posta/internal/jx"
)

func TestPublicEmbeddingTextAndNormalization(t *testing.T) {
	post := jx.M{"title":"Social media marketing","text":"Campaign example"}
	if publicEmbeddingText(post) == "" {
		t.Fatal("meaningful public text lost")
	}
	for _, key := range []string{"source_sensitive","nsfw","attachment_recovered","text_incomplete","media_incomplete","video_unavailable"} {
		p := jx.Copy(post)
		p[key] = true
		if publicEmbeddingText(p) != "" {
			t.Fatalf("unsafe embedding: %s",key)
		}
	}
	p := jx.Copy(post)
	p["fetched_via"] = "telegram_mtproto"
	if publicEmbeddingText(p) != "" || publicEmbeddingText(jx.M{"image":"https://example.com/image.jpg"}) != "" {
		t.Fatal("account-derived or textless content embedded as text")
	}
	v := make([]float64,512)
	v[0],v[1] = 3,4
	got := publicEmbeddingArray(v)
	if len(got)!=512 || math.Abs(float64(got[0])-.6)>1e-6 || math.Abs(float64(got[1])-.8)>1e-6 {
		t.Fatal("invalid normalization")
	}
	v[0] = math.NaN()
	if publicEmbeddingArray(v)!=nil || publicEmbeddingArray([]float64{1})!=nil {
		t.Fatal("malformed vector accepted")
	}
}

func TestPublicInventoryRankFusion(t *testing.T) {
	a:=publicInventoryCandidate{PlatformRSS,jx.M{"url":"https://example.com/a"}}
	b:=publicInventoryCandidate{PlatformRSS,jx.M{"url":"https://example.com/b"}}
	c:=publicInventoryCandidate{PlatformRSS,jx.M{"url":"https://example.com/c"}}
	got:=fusePublicInventory([]publicInventoryCandidate{a,b,b},[]publicInventoryCandidate{c,b})
	if len(got[PlatformRSS])!=3 || jx.Str(got[PlatformRSS][0],"url")!="https://example.com/b" {
		t.Fatalf("rank fusion failed: %v",got)
	}
}
