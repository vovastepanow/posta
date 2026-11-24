package posta

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotSMMCanonicalKeyKeepsLegacySources(t *testing.T) {
	legacy := fmt.Sprintf("v%d:%s", TopicProfileVersion, repeatDigest("smm"))
	for _, title := range []string{"smm", "SMM", "смм", "SMM и блогинг", "  smm  "} {
		if got := autopilotTopicKey(title); got != legacy {
			t.Errorf("%q got topic key %q, want existing %q", title, got, legacy)
		}
		if got := autopilotCanonicalTopic(title); got != "SMM и блогинг" {
			t.Errorf("%q canonical title = %q", title, got)
		}
	}
	if autopilotTopicKey("Маркетинг и реклама") == legacy {
		t.Fatal("separate marketing topic was merged into SMM")
	}
	if autopilotTopicKey("Блог врача") == legacy {
		t.Fatal("unrelated blog was merged into SMM")
	}
}

func TestAutopilotGeneralTaxonomyAliases(t *testing.T) {
	for _, tc := range []struct{ input, expected string }{
		{"маркетинг", "Маркетинг и реклама"},
		{"реклама", "Маркетинг и реклама"},
		{"Маркетинг и реклама", "Маркетинг и реклама"},
		{"smm", "SMM и блогинг"},
		{"блогинг", "SMM и блогинг"},
		{"Криптовалюта", "Криптовалюта"},
		{"крипта", "Криптовалюта"},
		{"Совершенно новая пользовательская тема", "Совершенно новая пользовательская тема"},
	} {
		if got := autopilotCanonicalTopic(tc.input); got != tc.expected {
			t.Errorf("%q canonicalized to %q, want %q", tc.input, got, tc.expected)
		}
		if autopilotTopicKey(tc.input) != autopilotTopicKey(tc.expected) {
			t.Errorf("different autopilot identity for %q and %q", tc.input, tc.expected)
		}
	}
	if autopilotTopicKey("SMM и блогинг") == autopilotTopicKey("Маркетинг и реклама") {
		t.Fatal("distinct tree categories were merged")
	}
}

func TestOKDiscoveryRequiresProfessionalIdentity(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Клуб вязания и рукоделия", false},
		{"Блог врача и здоровье", false},
		{"Новости знаменитостей и блогеров", false},
		{"SMM и продвижение в соцсетях", true},
		{"Таргетолог и реклама", true},
		{"Интернет-маркетинг для бизнеса", true},
	} {
		if got := okDiscoveryGroupTitleMatches(tc.title, "smm", []string{"блогинг", "бизнес"}); got != tc.want {
			t.Errorf("OK group title %q: got %t, want %t", tc.title, got, tc.want)
		}
	}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"Вязание, посты и видео из жизни блогера", false},
		{"Врач ведёт блог о пациентах", false},
		{"Скидка на рекламу в группе: обычный рекламный пост", false},
		{"Новый контент-план: анализ роста подписчиков в SMM", true},
		{"Кейс по таргетингу и продвижению соцсетей", true},
	} {
		// The matcher must never accept unrelated posts just because the
		// generated query contains generic words such as blog or business.
		if got := okDiscoveryTopicTextMatches(tc.text, "smm", []string{"блогинг", "бизнес"}); got != tc.want {
			t.Errorf("OK feed text %q: got %t, want %t", tc.text, got, tc.want)
		}
	}
}

func TestAutopilotOKSMMTopicReasonRequiresRealNegativeEvidence(t *testing.T) {
	const id = "12345678901234"
	post := func(text string) jx.M { return jx.M{"text": text} }
	unrelated := []jx.M{
		post("Собрали новые образцы вязания и схемы для рукоделия."),
		post("Новые советы по вязанию и вышивке крестиком."),
		post("Сегодня выбираем цвет пряжи для зимнего свитера."),
	}
	for _, tc := range []struct {
		name string
		title string
		posts []jx.M
		want string
	}{
		{"missing mobile title", id, unrelated, "source_metadata_unavailable"},
		{"off-topic title but no readable posts", "Клуб вязания", nil, "source_metadata_unavailable"},
		{"off-topic title and confirmed off-topic posts", "Клуб вязания", unrelated, "off_topic_group"},
		{"relevant group title", "SMM и продвижение в соцсетях", unrelated, ""},
		{"two independently relevant captions", "Новости сообщества", []jx.M{
			post("Новый контент-план: анализ роста подписчиков в SMM."),
			post("Разбор таргетинга и продвижения социальных сетей."),
			unrelated[0],
		}, ""},
		{"one relevant post is uncertain, not rejected", "Новости сообщества", []jx.M{
			post("Новый контент-план: анализ роста подписчиков в SMM."),
			unrelated[0], unrelated[1],
		}, "source_metadata_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autopilotOKSMMTopicReason(tc.title, id, tc.posts); got != tc.want {
				t.Fatalf("reason=%q, want=%q", got, tc.want)
			}
		})
	}
}

func TestAutopilotReconsiderHistoricOKRejectsOnlyOncePostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	job := &autopilotJob{Scope: autopilotAutoScope, Kind: autopilotAutoKind, Topic: "SMM и блогинг", TopicKey: autopilotTopicKey("SMM и блогинг")}
	source := fmt.Sprintf("ok:%019d", time.Now().UnixNano())
	future := time.Now().Add(30 * 24 * time.Hour).Unix()
	_, err = database.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources
		(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,checked_at,next_check_at)
		VALUES ($1,$2,$3,'rejected','off_topic_group',$4,$5,$5,$6)`,
		job.Scope, job.TopicKey, source, TopicProfileVersion, time.Now().Unix(), future)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, source)
	app := &App{DB: database}
	if err := app.autopilotRescheduleOldOKRejections(ctx, job); err != nil {
		t.Fatal(err)
	}
	var next int64
	var state string
	var marker bool
	if err := database.Pool.QueryRow(ctx, `SELECT next_check_at,state,COALESCE((evidence->>'ok_topic_reaudit')::boolean,false)
		FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3`,
		job.Scope, job.TopicKey, source).Scan(&next, &state, &marker); err != nil {
		t.Fatal(err)
	}
	if next > time.Now().Unix()+5 || state != "rejected" || !marker {
		t.Fatalf("not scheduled as unapproved recheck: next=%d state=%q marker=%v", next, state, marker)
	}
	_, err = database.Pool.Exec(ctx, `UPDATE source_autopilot_sources SET next_check_at=$4
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, source, future)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.autopilotRescheduleOldOKRejections(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := database.Pool.QueryRow(ctx, `SELECT next_check_at FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, source).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != future {
		t.Fatalf("historic source was rescheduled twice: next=%d future=%d", next, future)
	}
	// Keep this under the existing PostgreSQL workflow regression grouping.
	t.Run("bounded-full-context-rechecks", TestAutopilotScheduleFullContextRechecksPostgres)
}

func TestAutopilotScheduleFullContextRechecksPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner = "autopilot-context-recheck-owner"
	const topic = "SMM и блогинг"
	key := autopilotTopicKey(topic)
	defer func() {
		for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
			_, _ = d.Pool.Exec(context.Background(), "DELETE FROM "+table+" WHERE scope=$1 AND topic_key=$2", autopilotAutoScope, key)
		}
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_overrides WHERE scope=$1 AND topic_key=$2 AND source_id LIKE 'rss:https://example.org/context-recheck-%'`, autopilotAutoScope, key)
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM source_autopilot_demands WHERE user_id=$1`, owner)
		_, _ = d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	}()
	if _, err := d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	if err := saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{
		Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3,
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	a := &App{DB: d}
	if err := a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	at := time.Now().Unix()
	for i := 0; i < 23; i++ {
		source := fmt.Sprintf("rss:https://example.org/context-recheck-%d.xml", i)
		if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources
			(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,checked_at,next_check_at)
			VALUES($1,$2,$3,'rejected','preliminary_topic_mismatch',$4,$5,$5,$6)`,
			autopilotAutoScope, key, source, TopicProfileVersion, at, at+30*86400); err != nil {
			t.Fatal(err)
		}
	}
	blocked := "rss:https://example.org/context-recheck-0.xml"
	if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_overrides
		(scope,owner_id,topic_key,source_id,decision,updated_at)
		VALUES($1,'',$2,$3,'excluded',$4) ON CONFLICT DO NOTHING`,
		autopilotAutoScope, key, blocked, at); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := a.autopilotScheduleFullContextRechecks(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions
		WHERE scope=$1 AND topic_key=$2 AND reason='full_context_reaudit_scheduled'`,
		autopilotAutoScope, key).Scan(&count); err != nil || count != 20 {
		t.Fatalf("scheduled %d instead of daily cap 20, err=%v", count, err)
	}
	var blockedNext int64
	if err := d.Pool.QueryRow(ctx, `SELECT next_check_at FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, autopilotAutoScope, key, blocked).Scan(&blockedNext); err != nil || blockedNext != at+30*86400 {
		t.Fatalf("manual exclusion rescheduled: %d err=%v", blockedNext, err)
	}
	var changedState int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND state<>'rejected' AND source_id LIKE 'rss:https://example.org/context-recheck-%'`,
		autopilotAutoScope, key).Scan(&changedState); err != nil || changedState != 0 {
		t.Fatalf("scheduled rechecks must not approve or activate: %d err=%v", changedState, err)
	}
	// The v3 full audit must schedule every existing lifecycle state, not
	// just the old batch of twenty negative topic verdicts.
	for _, state := range []string{"candidate", "probation", "active", "cooldown"} {
		source := "rss:https://example.org/context-recheck-" + state + ".xml"
		resume, reason := "", "retain"
		if state == "cooldown" {
			resume, reason = "candidate", "rate_limited"
		}
		if _, err := d.Pool.Exec(ctx, `INSERT INTO source_autopilot_sources
			(scope,topic_key,source_id,state,resume_state,reason,policy_version,first_seen_at,checked_at,next_check_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8,$9)`,
			autopilotAutoScope, key, source, state, resume, reason, TopicProfileVersion, at, at+30*86400); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := a.autopilotQueueSourceReaudit(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions
		WHERE scope=$1 AND topic_key=$2 AND reason='source_reaudit_v3_scheduled'`,
		autopilotAutoScope, key).Scan(&count); err != nil || count != 26 {
		t.Fatalf("full audit scheduled %d instead of 26 eligible sources, err=%v", count, err)
	}
	var remaining int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND evidence ? 'review_policy_v3_scheduled'
		AND next_check_at > $3`, autopilotAutoScope, key, time.Now().Unix()).Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("expected only the rate-limited source to retain its backoff: %d err=%v", remaining, err)
	}
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_decisions
		WHERE scope=$1 AND topic_key=$2 AND reason='source_reaudit_v3_scheduled' AND old_state<>new_state`,
		autopilotAutoScope, key).Scan(&changedState); err != nil || changedState != 0 {
		t.Fatalf("full audit must preserve prior states until reviewed: %d err=%v", changedState, err)
	}
}
