package posta

import (
	"encoding/json"
	"os"
	"testing"

	"posta/internal/config"
	"posta/internal/jx"
)

// Решения реестра: каждый статус и каждая тема — без сети, кроме явно разрешённой перепроверки.
func TestRegistryDecide(t *testing.T) {
	const at = int64(1790000000)
	day := int64(config.DaySeconds)
	topic := "Мемы"
	key := TopicNorm(topic)
	rejected := &registryRow{Status: RegStatusRejected, Topics: map[string]registryTopic{key: {Score: 10, At: at - 5*day}}}
	cases := []struct {
		name string
		row  *registryRow
		want registryVerdict
		why  string
	}{
		{"нет в реестре", nil, regNew, ""},
		{"в каталоге — всегда", &registryRow{Status: RegStatusCatalog}, regNew, ""},
		{"предложен — не предлагать снова", &registryRow{Status: RegStatusSuggested}, regNew, ""},
		{"решение владельца «да»", &registryRow{Status: RegStatusOwnerYes}, regNew, ""},
		{"решение владельца «нет»", &registryRow{Status: RegStatusOwnerNo}, regNew, ""},
		{"«не знаю» владельца", &registryRow{Status: RegStatusOwnerMaybe}, regNew, ""},
		{"мёртвый", &registryRow{Status: RegStatusDead}, regSkip, "мёртвый, пустой или закрытый источник"},
		{"украинский", &registryRow{Status: RegStatusUkraine}, regSkip, "украинский источник"},
		{"не загрузился, повтор через сутки ещё рано", &registryRow{Status: RegStatusFailed, FailCount: 1, CheckedAt: at - day/2}, regSkip, "не загрузился — повтор через 1 дн."},
		{"не загрузился, сутки прошли", &registryRow{Status: RegStatusFailed, FailCount: 1, CheckedAt: at - 2*day}, regNew, ""},
		{"не загрузился трижды", &registryRow{Status: RegStatusFailed, FailCount: 3, CheckedAt: at - 2*day}, regSkip, "не загрузился — повтор через 4 дн."},
		{"отказ по этой теме — пропуск", rejected, regSkip, "уже проверен по этой теме и не подошёл"},
		{"отказ по другой теме, сэмпл свежий — без сети", &registryRow{Status: RegStatusRejected, HasSample: true, SampleAt: at - day}, regSample, ""},
		{"отказ по другой теме, сэмпл старый — пропуск", &registryRow{Status: RegStatusRejected, HasSample: true, SampleAt: at - 90*day}, regNew, ""},
		{"отказ по другой теме, сэмпла нет — пропуск", &registryRow{Status: RegStatusRejected}, regNew, ""},
	}
	for _, c := range cases {
		v, why := registryDecide(c.row, topic, at, 0, 60)
		if v != c.want || why != c.why {
			t.Errorf("%s: получили (%v, %q), ждали (%v, %q)", c.name, v, why, c.want, c.why)
		}
	}
	if v, _ := registryDecide(&registryRow{Status: RegStatusDead, Reason: "не загрузился трижды", FailCount: 3, CheckedAt: at - 8*day}, topic, at, 0, 60); v != regNew {
		t.Error("старые ошибочно заблокированные временные ошибки должны перепроверяться")
	}
	if v, _ := registryDecide(&registryRow{Status: RegStatusFailed, FailCount: 5, CheckedAt: at - 8*day}, topic, at, 0, 60); v != regNew {
		t.Error("временная ошибка должна повторяться после максимальной задержки")
	}
	// перепроверка отказа через registry_recheck_days — только если включена
	if v, _ := registryDecide(rejected, topic, at, 3, 60); v != regNew {
		t.Errorf("отказ 5 дней назад при перепроверке через 3 дня: ждали проверку по сети")
	}
	if v, _ := registryDecide(rejected, topic, at, 30, 60); v != regSkip {
		t.Errorf("отказ 5 дней назад при перепроверке через 30 дней: ждали пропуск")
	}
}

// Перенос старых данных на фикстуре: счётчики, приоритет статусов, ключи без темы.
func TestRegistryFromCatalogFixture(t *testing.T) {
	data, err := os.ReadFile("../../tools/fixtures/source_registry_old.json")
	if err != nil {
		t.Fatal(err)
	}
	var d SourceDB
	if err := json.Unmarshal(data, &d); err != nil {
		t.Fatal(err)
	}
	rows, cnt := registryFromCatalog(&d, 1790000500)
	if cnt.Catalog != 2 || cnt.Votes != 3 || cnt.Suggestions != 1 || cnt.Checked != 4 || cnt.Unkeyed != 1 {
		t.Errorf("счётчики: %+v", cnt)
	}
	if cnt.Rows != 7 || len(rows) != 7 {
		t.Errorf("строк %d, ждали 7", len(rows))
	}
	want := map[string]string{
		"telegram:gorodad": RegStatusOwnerYes,
		"telegram:memes_a": RegStatusCatalog,
		"telegram:memes_b": RegStatusOwnerNo,
		"telegram:memes_c": RegStatusOwnerMaybe,
		"telegram:memes_d": RegStatusSuggested,
		"telegram:memes_e": RegStatusRejected,
		"telegram:x":       RegStatusRejected,
	}
	for sid, status := range want {
		if r := rows[sid]; r == nil || r.Status != status {
			t.Errorf("%s: статус %v, ждали %s", sid, r, status)
		}
	}
	// тема в ключе без префикса — тема по умолчанию; пустая тема не должна остаться
	if _, ok := rows["telegram:x"].Topics[""]; ok {
		t.Errorf("ключ без темы попал в пустую тему")
	}
	if _, ok := rows["telegram:x"].Topics[TopicNorm(config.DefaultTopic)]; !ok {
		t.Errorf("ключ без темы не попал в тему по умолчанию")
	}
	// отказ по теме, которую источник уже предлагал, хранится по каждой теме отдельно
	if got := rows["telegram:memes_d"].Topics[TopicNorm("Мемы")]; got.Score != 62 {
		t.Errorf("балл предложения по теме: %+v", got)
	}
	if got := rows["telegram:memes_a"].Topics[TopicNorm("Мемы")]; got.Reason != registryMovedReason {
		t.Errorf("перенесённая оценка без причины: %+v", got)
	}
}

// platformOfSourceID — по префиксу ключа, в том числе имиджборды.
func TestPlatformOfSourceID(t *testing.T) {
	for sid, want := range map[string]string{"telegram:x": PlatformTelegram, "vk:club1": PlatformVK, "4chan:wsg:1": Platform4chan, "nope": ""} {
		if got := platformOfSourceID(sid); got != want {
			t.Errorf("%q → %q, ждали %q", sid, got, want)
		}
	}
}

func TestSourceRejectionIsTopicScoped(t *testing.T) {
	u := &U{App: &App{catalog: &Catalog{idx: buildTopicIndex(nil)}}}
	d := &SourceDB{Telegram: []jx.M{{"id": "example", "name": "Example"}}}
	source := jx.M{"id": "example", "platform": PlatformTelegram}
	u.removeRejectedSource(d, source, "Мемы")
	if len(d.Telegram) != 1 || u.CatalogEntryMatchesTopic(d.Telegram[0], "Мемы", true) || !u.CatalogEntryMatchesTopic(d.Telegram[0], "Новости", true) {
		t.Fatalf("отказ должен исключать только одну тему: %#v", d.Telegram)
	}
	d.Votes = []jx.M{{"source_id": "telegram:example", "vote": "reject", "topic": "Мемы"}, {"source_id": "telegram:other", "vote": "reject", "topic": "Мемы"}}
	if !u.discoveryTopicKnown(d, "Мемы")["telegram:other"] || u.discoveryTopicKnown(d, "Новости")["telegram:other"] {
		t.Fatal("голос должен учитываться только в своей теме")
	}
	if !u.addManualCatalogSource(d, PlatformTelegram, "example", "Example", "Мемы", nil) {
		t.Fatal("explicit re-add did not restore excluded topic")
	}
	if !u.CatalogEntryMatchesTopic(d.Telegram[0], "Мемы", true) || !u.CatalogEntryMatchesTopic(d.Telegram[0], "Новости", true) {
		t.Fatal("повторное добавление должно вернуть источник в тему")
	}
}

func TestThreadRemovalPreservesHistoryAndOtherTopics(t *testing.T) {
	u := taxonomyTestUser()
	const sid = "4chan:g:123"
	d := &SourceDB{
		Suggestions: []jx.M{{"source_id": sid, "topic": "Мемы", "origin": jx.M{"kind": "mentioned"}}},
		Votes: []jx.M{
			{"source_id": sid, "topic": "Мемы", "vote": "approve", "scope": "thread"},
			{"source_id": sid, "topic": "Новости", "vote": "approve", "scope": "thread"},
		},
	}
	source := jx.M{"source_id": sid, "platform": Platform4chan, "board": "g", "thread_id": "123"}
	record, err := sourceRejectionRecord(source)
	if err != nil {
		t.Fatal(err)
	}
	record["topic"], record["scope"], record["reason"] = "Мемы", "thread", "other"
	u.applyTopicSourceRejection(d, record, source, "Мемы")
	if len(d.Suggestions) != 1 || len(d.Votes) != 2 || jx.Str(d.Votes[0], "topic") != "Новости" || jx.Str(d.Votes[0], "vote") != "approve" ||
		jx.Str(d.Votes[1], "vote") != "reject" || jx.Str(d.Votes[1], "origin_kind") != "mentioned" {
		t.Fatalf("thread removal damaged source history: %#v %#v", d.Suggestions, d.Votes)
	}
}
