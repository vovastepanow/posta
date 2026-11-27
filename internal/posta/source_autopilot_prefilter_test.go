package posta

import (
	"math"
	"testing"
)

func TestSourceEmbeddingCacheAndCosine(t *testing.T) {
	a := unpackSourceEmbedding(packSourceEmbedding([]float64{3, 4}))
	b := unpackSourceEmbedding(packSourceEmbedding([]float64{0, 5}))
	got, ok := sourceEmbeddingCosine(a, b)
	if !ok || math.Abs(got-.8) > .0001 {
		t.Fatalf("cosine %v, %v", got, ok)
	}
	for _, v := range [][]float64{nil, {0, 0}, {math.NaN(), 1}, {math.Inf(1), 0}} {
		if packSourceEmbedding(v) != nil {
			t.Fatalf("accepted invalid vector %v", v)
		}
	}
	if sourceEmbeddingKey("a", "text") == sourceEmbeddingKey("b", "text") {
		t.Fatal("model changes must invalidate the cache key")
	}
	if _, ok := sourceEmbeddingCosine(a, []float64{1}); ok {
		t.Fatal("accepted different dimensions")
	}
}

func TestSourceEmbeddingPrescreenNeverDiscardsMixedOrUncertain(t *testing.T) {
	low := &sourceEmbeddingScore{Model: "test", Posts: 6, Max: .20, Mean: .05}
	if !sourceEmbeddingShouldDefer(low, 25, true, 6) {
		t.Fatal("clearly low text-only candidate should be deferred once")
	}
	if sourceEmbeddingShouldDefer(low, 25, false, 6) || sourceEmbeddingShouldDefer(low, 25, true, 10) ||
		sourceEmbeddingShouldDefer(low, 0, true, 6) || sourceEmbeddingShouldDefer(nil, 25, true, 6) {
		t.Fatal("visual, incomplete, disabled or unknown sample deferred")
	}
	low.LexicalMatch = true
	if sourceEmbeddingShouldDefer(low, 25, true, 6) {
		t.Fatal("lexical evidence ignored")
	}
	if sourceEmbeddingLexicalMatch([]string{"smm"}, []string{"womensmma"}) ||
		!sourceEmbeddingLexicalMatch([]string{"smm"}, []string{"SMM: реальный кейс"}) {
		t.Fatal("lexical matching must use whole tokens")
	}
	v := [][]float64{{1, 0}, {0, 1}, {1, 0}, {0, 1}, {0, 1}, {0, 1}}
	mixed := sourceEmbeddingSummarize("test", v, false)
	if mixed == nil || mixed.Max != 1 || sourceEmbeddingShouldDefer(mixed, 25, true, 5) {
		t.Fatal("a good post must prevent deferral even if the others are irrelevant")
	}
}
