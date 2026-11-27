package posta

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"

	"posta/internal/jx"
)

const sourceEmbeddingTTL = 7 * 24 * time.Hour

// Cache by the actual model and normalized input, not by topic or source.
// Float32 binary encoding avoids large JSON arrays; values expire via CacheGC.
func sourceEmbeddingKey(model, text string) string {
	return "source_embedding:v3:document:512:" + repeatDigest(model+"\x00"+text)
}

func packSourceEmbedding(v []float64) []byte {
	if len(v) == 0 || len(v) > 4096 {
		return nil
	}
	var norm float64
	for _, x := range v {
		norm += x * x
	}
	if norm <= 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return nil
	}
	norm = math.Sqrt(norm)
	out := make([]byte, len(v)*4)
	for i, x := range v {
		binary.LittleEndian.PutUint32(out[i*4:], math.Float32bits(float32(x / norm)))
	}
	return out
}

func unpackSourceEmbedding(raw []byte) []float64 {
	if len(raw) == 0 || len(raw)%4 != 0 || len(raw) > 4096*4 {
		return nil
	}
	v := make([]float64, len(raw)/4)
	var norm float64
	for i := range v {
		v[i] = float64(math.Float32frombits(binary.LittleEndian.Uint32(raw[i*4:])))
		norm += v[i] * v[i]
	}
	if math.IsNaN(norm) || math.IsInf(norm, 0) || math.Abs(norm-1) > .01 {
		return nil
	}
	return v
}

func sourceEmbeddingCosine(a, b []float64) (float64, bool) {
	if len(a) == 0 || len(a) != len(b) {
		return 0, false
	}
	var dot float64
	for i := range a {
		dot += a[i] * b[i]
	}
	if math.IsNaN(dot) || math.IsInf(dot, 0) {
		return 0, false
	}
	return max(-1, min(1, dot)), true
}

type sourceEmbeddingScore struct {
	Model        string  `json:"model"`
	Posts        int     `json:"posts"`
	Max          float64 `json:"max_cosine"`
	Mean         float64 `json:"mean_cosine"`
	LexicalMatch bool    `json:"lexical_match"`
}

func sourceEmbeddingSummarize(model string, vectors [][]float64, lexical bool) *sourceEmbeddingScore {
	if len(vectors) < 3 {
		return nil
	}
	score := &sourceEmbeddingScore{Model: model, Posts: len(vectors) - 1, Max: -1, LexicalMatch: lexical}
	for _, v := range vectors[1:] {
		s, ok := sourceEmbeddingCosine(vectors[0], v)
		if !ok {
			return nil
		}
		score.Max = max(score.Max, s)
		score.Mean += s
	}
	score.Mean /= float64(score.Posts)
	return score
}

func sourceEmbeddingWords(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// Lexical overlap is a safety veto against deferral, not an admission gate.
// A missing keyword never independently proves that a source is irrelevant.
func sourceEmbeddingLexicalMatch(anchors []string, captions []string) bool {
	words := map[string]bool{}
	for _, anchor := range anchors {
		for _, word := range sourceEmbeddingWords(anchor) {
			if len([]rune(word)) >= 3 && word != "для" && word != "the" && word != "and" {
				words[word] = true
			}
		}
	}
	for _, caption := range captions {
		for _, word := range sourceEmbeddingWords(caption) {
			if words[word] {
				return true
			}
		}
	}
	return false
}

// Do not collapse a mixed publisher into one vector. Positive topic context
// excludes negative examples, and each sufficiently readable post is encoded
// separately. An empty or partial text stays unknown.
func (a *App) sourceEmbeddingInputs(topic string, posts []jx.M) (string, []string, []string) {
	positive := []string{topic}
	anchors := []string{topic}
	for _, name := range TopicParts(topic) {
		t := a.TopicLookup(TopicBase(name))
		if t == nil {
			continue
		}
		positive = append(positive, t.Title, t.About)
		anchors = append(anchors, t.Title)
		anchors = append(anchors, t.Aliases...)
		anchors = append(anchors, t.Queries...)
		if t.Profile != nil {
			anchors = append(anchors, t.Profile.Queries...)
		}
	}
	anchors = autopilotCleanDiscoveryQueries(topic, anchors)
	var captions []string
	seen := map[string]bool{}
	for _, p := range posts[:min(10, len(posts))] {
		if jx.Bool(p, "text_incomplete") {
			continue
		}
		caption := sourceReviewCaption(jx.Str(p, "text"))
		if caption == "" || caption == EmptyPostText || len([]rune(caption)) < 40 ||
			len(sourceEmbeddingWords(caption)) < 5 || seen[caption] {
			continue
		}
		seen[caption] = true
		captions = append(captions, caption)
	}
	return trimRunes(strings.Join(positive, ". "), 1200), anchors, captions
}

func (a *App) autopilotEmbeddingPrescreen(ctx context.Context, job *autopilotJob, info jx.M, posts []jx.M) (*sourceEmbeddingScore, string) {
	target := a.LLM.SourceEmbeddingTarget()
	if target.Empty() {
		return nil, "openrouter_embedding_disabled"
	}
	query, anchors, captions := a.sourceEmbeddingInputs(job.Topic, posts)
	if len(captions) < 5 || len(captions)*3 < min(10, len(posts))*2 {
		return nil, "insufficient_text"
	}
	// The publisher's metadata provides a safety veto, but cannot override
	// the meaning of its actual posts or act as a quality score.
	lexical := sourceEmbeddingLexicalMatch(anchors, append(append([]string(nil), captions...), jx.Str(info, "title"), jx.Str(info, "description")))
	if lexical {
		// A positive anchor already prevents deferral; no need to buy vectors.
		return nil, "lexical_match"
	}
	texts := append([]string{query}, captions...)
	vectors := make([][]float64, len(texts))
	var missing []string
	var indexes []int
	for i, s := range texts {
		var raw []byte
		if a.CacheGet(ctx, sourceEmbeddingKey(target.String(), s), &raw) {
			vectors[i] = unpackSourceEmbedding(raw)
		}
		if vectors[i] == nil {
			missing = append(missing, s)
			indexes = append(indexes, i)
		}
	}
	if len(missing) > 0 {
		unavailableKey := "source_embedding_unavailable:" + target.String()
		if a.Mem != nil {
			if _, unavailable := a.Mem.Get(unavailableKey); unavailable {
				return nil, "openrouter_embedding_unavailable"
			}
		}
		// Limit free embedding calls independently, preserving most of the
		// shared provider quota for the actual quality classifier.
		at, id := time.Now(), randHex(16)
		limit := min(int64(20), int64(a.LLM.Settings().FreeDailyLimit)/10)
		if limit <= 0 {
			return nil, "embedding_budget_disabled"
		}
		err := withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
			if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
				return err
			}
			if err := reserveAutopilotBudgetTx(ctx, tx, id+":server", "server", "source_embedding", at, 1, limit); err != nil {
				return err
			}
			return reserveAutopilotBudgetTx(ctx, tx, id+":topic", autopilotBudgetTopicScope(job), "source_embedding", at, 1, min(int64(8), limit))
		})
		if errors.Is(err, errAutopilotBudgetExhausted) {
			return nil, "embedding_budget_exhausted"
		}
		if err != nil {
			return nil, "embedding_budget_unavailable"
		}
		embedCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		got, err := a.LLM.EmbedSource(embedCtx, target, missing)
		cancel()
		if err != nil || len(got) != len(missing) {
			if a.Mem != nil {
				a.Mem.Set(unavailableKey, true, 10*time.Minute)
			}
			return nil, "openrouter_embedding_unavailable"
		}
		for j, i := range indexes {
			raw := packSourceEmbedding(got[j])
			vectors[i] = unpackSourceEmbedding(raw)
			if vectors[i] == nil {
				return nil, "invalid_vector"
			}
			a.CacheSet(ctx, sourceEmbeddingKey(target.String(), texts[i]), raw, sourceEmbeddingTTL)
		}
	}
	score := sourceEmbeddingSummarize(target.String(), vectors, lexical)
	if score == nil {
		return nil, "invalid_vector"
	}
	return score, "scored"
}

func sourceEmbeddingShouldDefer(score *sourceEmbeddingScore, threshold int, textOnly bool, samples int) bool {
	return score != nil && threshold > 0 && textOnly && samples >= 5 &&
		score.Posts >= 5 && score.Posts*3 >= samples*2 && !score.LexicalMatch &&
		score.Max <= float64(threshold)/100
}

func (a *App) autopilotEmbeddingEligible(ctx context.Context, topic string, posts []jx.M) bool {
	if len(posts) < 5 {
		return false
	}
	u := a.User(ctx, "", nil)
	format := u.TopicFormatOf(topic)
	textOnly := format.Media == "optional" && format.Judge != "image" && !sourcePostsHaveImages(posts)
	for _, post := range posts {
		if PostItemHasVideo(post) || jx.Str(post, "audio") != "" || len(jx.List(post, "source_attachments")) > 0 ||
			len(jx.List(post, "gallery")) > 0 || len(jx.List(post, "media")) > 0 {
			textOnly = false
			break
		}
	}
	return textOnly
}
