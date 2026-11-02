package posta

import (
	"encoding/json"
	"os"
	"testing"

	"posta/internal/config"
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
		{"в каталоге — всегда", &registryRow{Status: RegStatusCatalog}, regSkip, "уже в каталоге"},
		{"предложен — не предлагать снова", &registryRow{Status: RegStatusSuggested}, regSkip, "уже предлагался"},
		{"решение владельца «да»", &registryRow{Status: RegStatusOwnerYes}, regSkip, "владелец одобрил"},
		{"решение владельца «нет»", &registryRow{Status: RegStatusOwnerNo}, regSkip, "владелец отклонил"},
		{"«не знаю» владельца", &registryRow{Status: RegStatusOwnerMaybe}, regSkip, "владелец отметил «не знаю»"},
		{"мёртвый", &registryRow{Status: RegStatusDead}, regSkip, "мёртвый, пустой или закрытый источник"},
		{"украинский", &registryRow{Status: RegStatusUkraine}, regSkip, "украинский источник"},
		{"не загрузился, повтор через сутки ещё рано", &registryRow{Status: RegStatusFailed, FailCount: 1, CheckedAt: at - day/2}, regSkip, "не загрузился — повтор через сутки"},
		{"не загрузился, сутки прошли", &registryRow{Status: RegStatusFailed, FailCount: 1, CheckedAt: at - 2*day}, regNew, ""},
		{"не загрузился трижды", &registryRow{Status: RegStatusFailed, FailCount: 3, CheckedAt: at - 2*day}, regSkip, "не загрузился трижды"},
		{"отказ по этой теме — пропуск", rejected, regSkip, "уже проверен по этой теме и не подошёл"},
		{"отказ по другой теме, сэмпл свежий — без сети", &registryRow{Status: RegStatusRejected, HasSample: true, SampleAt: at - day}, regSample, ""},
		{"отказ по другой теме, сэмпл старый — пропуск", &registryRow{Status: RegStatusRejected, HasSample: true, SampleAt: at - 90*day}, regSkip, "отклонён по другой теме, сохранённый сэмпл устарел"},
		{"отказ по другой теме, сэмпла нет — пропуск", &registryRow{Status: RegStatusRejected}, regSkip, "отклонён по другой теме, сохранённый сэмпл устарел"},
	}
	for _, c := range cases {
		v, why := registryDecide(c.row, topic, at, 0, 60)
		if v != c.want || why != c.why {
			t.Errorf("%s: получили (%v, %q), ждали (%v, %q)", c.name, v, why, c.want, c.why)
		}
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
