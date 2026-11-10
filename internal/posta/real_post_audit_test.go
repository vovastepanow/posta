package posta

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

type realPostAuditCase struct {
	ID, Fixture, Topic, Note, Text, URL, Image string
	Index                                      int
	Want                                       bool
}

// This opt-in replay uses public source snapshots and the configured model and
// prompts. It never starts collectors or publishes, and PostgreSQL is read-only.
func TestRealPostAudit(t *testing.T) {
	if os.Getenv("POSTA_REAL_AUDIT") != "1" {
		t.Skip("set POSTA_REAL_AUDIT=1 and POSTA_ROOT to replay public posts with the configured model")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	cfg := config.LoadServer()
	pgcfg, err := pgxpool.ParseConfig(cfg.PostgresDSN)
	if err != nil {
		t.Fatal("invalid database configuration")
	}
	pgcfg.ConnConfig.RuntimeParams["default_transaction_read_only"] = "on"
	pool, err := pgxpool.NewWithConfig(ctx, pgcfg)
	if err != nil {
		t.Fatal("database unavailable")
	}
	defer pool.Close()
	a := taxonomyTestUser().App
	a.DB, a.Cfg, a.ctx = &db.DB{Pool: pool}, cfg, ctx
	settings := llm.Preset("free")
	a.KVGet(ctx, "admin:llm", &settings)
	// The replay is restricted to free/local models, including on fallback.
	for role, rc := range settings.Roles {
		if !rc.Primary.Empty() && !rc.Primary.Free() {
			t.Fatalf("paid primary configured for %s", role)
		}
		if !rc.Fallback.Empty() && !rc.Fallback.Free() {
			t.Fatalf("paid fallback configured for %s", role)
		}
	}
	a.LLM = llm.NewRouter(llm.NewOllama(cfg.OllamaURL), llm.NewOpenRouter(cfg.OpenRouterURL, cfg.OpenRouterKey, cfg.OpenRouterTitle, cfg.PublicURL))
	a.LLM.LoadSettings = func() llm.Settings { return settings }
	if rawPath := os.Getenv("POSTA_REAL_AUDIT_RAW"); rawPath != "" {
		calls, openErr := os.Create(rawPath)
		if openErr != nil {
			t.Fatal(openErr)
		}
		defer calls.Close()
		a.LLM.OnCall = func(call llm.Call) {
			if call.Resp != nil {
				json.NewEncoder(calls).Encode(jx.M{"role": call.Role, "topic": call.Req.Topic, "text": call.Resp.Text})
			}
		}
	}
	// No application accounting callbacks: the replay cannot mutate records.
	var auditUser string
	a.DB.Pool.QueryRow(ctx, `SELECT id FROM users WHERE adult_allowed=true LIMIT 1`).Scan(&auditUser)
	u := a.User(ctx, auditUser, nil)
	casePath := os.Getenv("POSTA_REAL_AUDIT_CASES")
	if casePath == "" {
		casePath = "testdata/real_post_audit_cases.json"
	}
	if os.Getenv("POSTA_REAL_AUDIT_QUEUE") == "1" {
		videoJSON, readErr := os.ReadFile("testdata/real_video_candidates.json")
		if readErr != nil {
			t.Fatal(readErr)
		}
		var videos []jx.M
		if readErr = json.Unmarshal(videoJSON, &videos); readErr != nil {
			t.Fatal(readErr)
		}
		// Freeze freshness at replay time: this check isolates the original
		// format rejection, not the later aging of a saved public snapshot.
		for _, video := range videos {
			video["posted_at"] = now()
		}
		format := u.TopicFormatOf("Мемы")
		queue, skipped := u.buildCandidateQueue(map[string][]jx.M{PlatformJoyreactor: videos}, &knownKeys{keys: map[string]bool{}}, map[string]bool{}, map[string][2]int{}, &format, jx.M{}, "Мемы")
		if len(queue) != len(videos) {
			t.Errorf("real videos lost before analysis: got=%d want=%d skipped=%v", len(queue), len(videos), skipped)
		}
		t.Logf("real video candidates: queued=%d format_rejected=%d", len(queue), skipped["format"])
	}
	raw, err := os.ReadFile(casePath)
	if err != nil {
		t.Fatal(err)
	}
	var cases []realPostAuditCase
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	outPath := os.Getenv("POSTA_REAL_AUDIT_OUTPUT")
	if outPath == "" {
		outPath = filepath.Join(t.TempDir(), "real-post-audit.jsonl")
	}
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	enc := json.NewEncoder(out)
	mismatches, evaluated := 0, 0
	// Batch by topic as the collector does; maintain deterministic topic order.
	var topics []string
	groups := map[string][]realPostAuditCase{}
	for _, c := range cases {
		if filter := os.Getenv("POSTA_REAL_AUDIT_TOPIC"); filter != "" && c.Topic != filter {
			continue
		}
		if _, ok := groups[c.Topic]; !ok {
			topics = append(topics, c.Topic)
		}
		groups[c.Topic] = append(groups[c.Topic], c)
	}
	for _, topic := range topics {
		batch := groups[topic]
		texts := make([]string, len(batch))
		for i, c := range batch {
			texts[i] = c.Text
		}
		started := time.Now()
		var analysis []Analysis
		var callErr error
		quick := map[int]QuickJunk{}
		if batch[0].Image != "" {
			analysis = make([]Analysis, len(batch))
			for i, c := range batch {
				data, readErr := os.ReadFile(c.Image)
				if readErr != nil {
					t.Fatal(readErr)
				}
				item := jx.M{"image": c.Image, "url": c.URL, "text": c.Text}
				quick[i] = u.ParallelQuickScores([]prescreenRow{{Item: item, Poster: data}})[0]
				an, _, visionErr := u.analyzeCandidateVision([][]byte{data}, item, EmptyHistory(), topic, nil)
				if visionErr != nil {
					t.Logf("vision error: %v", visionErr)
				}
				analysis[i] = an
			}
		} else {
			analysis, callErr = u.AnalyzeTextBatch(texts, topic, EmptyHistory())
		}
		for i, c := range batch {
			record := jx.M{"id": c.ID, "fixture": c.Fixture, "index": c.Index, "url": c.URL, "topic": topic, "want": c.Want, "note": c.Note}
			if callErr != nil || analysis[i] == nil {
				record["error"] = "no valid analysis"
				if callErr != nil {
					record["error"] = callErr.Error()
				}
				t.Errorf("case %s (%s): %v", c.ID, topic, record["error"])
			} else {
				an := analysis[i]
				item := jx.M{"url": c.URL, "source_url": c.URL, "text": c.Text, "text_post": true}
				if c.Image != "" {
					item["image"], item["text_post"] = c.Image, false
				}
				reason := u.contentAdmissionReason(an, item, topic, c.Text)
				if q, ok := quick[i]; ok {
					record["prescreen_score"] = q.Score
					if u.prescreenIsJunk(q.Score, item) {
						reason = "быстрая проверка: мусор"
					}
				}
				accepted := reason == ""
				record["analysis"], record["accepted"], record["reason"] = an, accepted, reason
				record["match"] = accepted == c.Want
				evaluated++
				if accepted != c.Want {
					mismatches++
					t.Logf("MISMATCH case=%s topic=%s want=%v reason=%s relevance=%v advertising=%v", c.ID, topic, c.Want, reason, an["relevance"], an["advertising"])
				}
			}
			if err := enc.Encode(record); err != nil {
				t.Fatal(err)
			}
		}
		out.Sync()
		t.Logf("topic=%s cases=%d elapsed=%s", topic, len(batch), time.Since(started).Round(time.Millisecond))
	}
	t.Logf("evaluated=%d mismatches=%d output=%s", evaluated, mismatches, outPath)
	if mismatches != 0 {
		t.Errorf("%d real-post decisions differ from reviewed expectations", mismatches)
	}
}
