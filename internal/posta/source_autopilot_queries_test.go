package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestAutopilotGeneratesQueriesThroughModelGate(t *testing.T) {
	calls, gates := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		_ = json.NewEncoder(w).Encode(jx.M{"choices": []any{jx.M{"message": jx.M{"content": `{"queries":["новый вариант"]}`}}}})
	}))
	defer server.Close()
	a := taxonomyTestUser().App
	a.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(server.URL, "test", "", ""))
	a.LLM.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.Roles[llm.RoleSmart] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "example:free"}}
		return s
	}
	ctx := llm.WithCallGate(context.Background(), func(context.Context, llm.Target) error { gates++; return nil })
	job := &autopilotJob{Scope: autopilotAutoScope, Kind: autopilotAutoKind, Topic: "Необычная тема", TopicKey: autopilotTopicKey("Необычная тема"), Checkpoint: []byte(`{}`)}
	if _, err := a.autopilotChooseQuery(ctx, job); err != nil {
		t.Fatal(err)
	}
	checkpoint := jx.Unmarshal(job.Checkpoint)
	if calls < 1 || gates != calls || len(jx.Strs(checkpoint, "generated_queries")) != 1 {
		t.Fatalf("calls=%d gates=%d checkpoint=%+v", calls, gates, checkpoint)
	}
}

// A transient OpenRouter failure must not mark query generation as successful
// for seven days. The following run should retry without exhausting the
// discovery worker's existing local query options.
func TestAutopilotQueryGenerationRetriesAfterProviderFailure(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":{"message":"temporarily unavailable"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(jx.M{"choices": []any{jx.M{"message": jx.M{"content": `{"queries":["новый поисковый запрос"]}`}}}})
	}))
	defer server.Close()
	a := taxonomyTestUser().App
	a.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(server.URL, "test", "", ""))
	a.LLM.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.Roles[llm.RoleSmart] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "example:free"}, Fallback: llm.Off}
		return s
	}
	job := &autopilotJob{Scope: autopilotAutoScope, Kind: autopilotAutoKind, Topic: "Необычная тема", TopicKey: autopilotTopicKey("Необычная тема"), Checkpoint: []byte(`{}`)}
	if _, err := a.autopilotChooseQuery(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	failed := jx.Unmarshal(job.Checkpoint)
	if jx.Int(failed, "query_generated_at") != 0 || jx.Int(failed, "query_attempted_at") == 0 {
		t.Fatalf("failed provider call must store retry backoff, not a successful generation: %+v", failed)
	}
	attempts := calls
	if _, err := a.autopilotChooseQuery(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	if calls != attempts {
		t.Fatalf("provider retried during one-hour backoff: before=%d after=%d", attempts, calls)
	}
	failed = jx.Unmarshal(job.Checkpoint)
	failed["query_attempted_at"] = time.Now().Add(-2 * time.Hour).Unix()
	job.Checkpoint = jx.Marshal(failed)
	if _, err := a.autopilotChooseQuery(context.Background(), job); err != nil {
		t.Fatal(err)
	}
	recovered := jx.Unmarshal(job.Checkpoint)
	if calls < 2 || jx.Int(recovered, "query_generated_at") == 0 || len(jx.Strs(recovered, "generated_queries")) != 1 {
		t.Fatalf("calls=%d checkpoint=%+v", calls, recovered)
	}
}

func TestAutopilotQueryUsesObservedYieldAndExploresPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("set POSTA_AUTOPILOT_TEST_DSN to an isolated database")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const topic, scope = "Тематика эксперимента", "test-query-learning"
	key := autopilotTopicKey(topic)
	for _, table := range []string{"source_autopilot_query_sources", "source_autopilot_post_evidence", "source_autopilot_sources", "source_autopilot_collection_passes"} {
		if _, err = d.Pool.Exec(ctx, `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, scope, key); err != nil {
			t.Fatal(err)
		}
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope=$1 AND topic_key=$2`, scope, key)
	}
	a := &App{DB: d, catalog: &Catalog{idx: buildTopicIndex([]*Topic{{ID: "test", Title: topic, Queries: []string{"вариант альфа", "вариант бета"}}})}}
	job := &autopilotJob{Scope: scope, Topic: topic, TopicKey: key, Checkpoint: []byte(`{}`)}
	query, err := a.autopilotChooseQuery(ctx, job)
	if err != nil || query != strings.ToLower(topic) {
		t.Fatalf("first exploration: %q %v", query, err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_query_sources(scope,topic_key,query,source_id,discovered_at) VALUES($1,$2,'вариант альфа','rss:test',1)`, scope, key); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,policy_version,first_seen_at) VALUES($1,$2,'rss:test','active',1,1)`, scope, key); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising) VALUES($1,$2,'rss:test','post','content',1,$3,$3,true,true,false)`, scope, key, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	observed, err := a.autopilotObservedYield(ctx, job)
	if err != nil || len(observed) != 0 {
		t.Fatalf("insufficient coverage counted: %+v %v", observed, err)
	}
	stamp := time.Now().Unix()
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_collection_passes(scope,topic_key,source_id,slot,fetched_at,fetched_count) VALUES($1,$2,'rss:test',1,$3,1),($1,$2,'rss:test',2,$4,1)`, scope, key, stamp, stamp-86400); err != nil {
		t.Fatal(err)
	}
	observed, err = a.autopilotObservedYield(ctx, job)
	if err != nil || observed["rss:test"] != 1.0/7.0 {
		t.Fatalf("covered yield: %+v %v", observed, err)
	}
	query, err = a.autopilotChooseQuery(ctx, job)
	if err != nil || query != "вариант альфа" {
		t.Fatalf("observed yield ignored: %q %v", query, err)
	}
	if _, err = d.Pool.Exec(ctx, `UPDATE source_autopilot_sources SET state='rejected' WHERE scope=$1 AND topic_key=$2 AND source_id='rss:test'`, scope, key); err != nil {
		t.Fatal(err)
	}
	query, err = a.autopilotChooseQuery(ctx, job)
	if err != nil || query != strings.ToLower(topic) {
		t.Fatalf("rejected source still rewarded: %q %v", query, err)
	}
	if _, err = d.Pool.Exec(ctx, `UPDATE source_autopilot_sources SET state='active' WHERE scope=$1 AND topic_key=$2 AND source_id='rss:test'`, scope, key); err != nil {
		t.Fatal(err)
	}
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_autopilot_post_evidence(scope,topic_key,source_id,post_hash,content_hash,policy_version,published_at,observed_at,relevant,eligible,advertising) VALUES($1,$2,'rss:clone','clone','content',1,$3,$3,true,true,false)`, scope, key, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	query, err = a.autopilotChooseQuery(ctx, job)
	if err != nil || query != strings.ToLower(topic) {
		t.Fatalf("duplicated content rewarded as unique yield: %q %v", query, err)
	}
	job.Checkpoint = []byte(`{"query_turn":8}`)
	query, err = a.autopilotChooseQuery(ctx, job)
	if err != nil || query != "вариант бета" {
		t.Fatalf("exploration stopped: %q %v", query, err)
	}
	job.Checkpoint = []byte(`{"query_turn":12,"generated_queries":["вариант гамма"]}`)
	query, err = a.autopilotChooseQuery(ctx, job)
	if err != nil || query != "вариант гамма" {
		t.Fatalf("generated variant ignored: %q %v", query, err)
	}
}

func TestAutopilotQueryQualityPrefersCleanSourcesOverRawVolume(t *testing.T) {
	clean := autopilotQueryQualityScore(8, 4, 0)
	noisy := autopilotQueryQualityScore(500, 4, 100)
	if clean <= noisy {
		t.Fatalf("raw post volume outweighed source precision: clean=%d noisy=%d", clean, noisy)
	}
	if autopilotQueryQualityScore(0, 0, 0) != 0 || autopilotQueryQualityScore(0, 0, 20) != 0 {
		t.Fatal("unknown or bad queries must not gain a positive yield score")
	}
	if autopilotQueryQualityScore(8, 4, 0) <= autopilotQueryQualityScore(8, 1, 0) {
		t.Fatal("independent useful sources must beat one prolific source")
	}
}
