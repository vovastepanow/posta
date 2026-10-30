package posta

import (
	"encoding/json"
	"os"
	"testing"

	"posta/internal/config"
)

// Годнота на фикстурах: живой оригинальный канал выше агрегатора с рекламой, мёртвого и перезаливщика.
func TestSourceGoodnessFixtures(t *testing.T) {
	data, err := os.ReadFile("../../tools/fixtures/source_goodness_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name string `json:"name"`
			In   struct {
				ViewRate     float64 `json:"view_rate"`
				ViewMedian   float64 `json:"view_median"`
				LastPostDays float64 `json:"last_post_age_days"`
				PerWeek      float64 `json:"per_week"`
				AdShare      float64 `json:"ad_share"`
				LinkShare    float64 `json:"link_share"`
				HasPosts     bool    `json:"has_posts"`
				Taste        *int    `json:"taste"`
			} `json:"in"`
			Min *int `json:"min"`
			Max *int `json:"max"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, c := range file.Cases {
		rate := c.In.ViewRate
		res := SourceGoodness(GoodnessInput{ViewRate: &rate, ViewMedian: c.In.ViewMedian, LastPostAge: int64(c.In.LastPostDays * config.DaySeconds),
			PerWeek: c.In.PerWeek, AdShare: c.In.AdShare, LinkShare: c.In.LinkShare, HasPosts: c.In.HasPosts, Taste: c.In.Taste})
		got[c.Name] = res.Score
		if c.Min != nil && res.Score < *c.Min {
			t.Errorf("%s: годнота %d, ждали не ниже %d (%s)", c.Name, res.Score, *c.Min, res.Note)
		}
		if c.Max != nil && res.Score > *c.Max {
			t.Errorf("%s: годнота %d, ждали не выше %d (%s)", c.Name, res.Score, *c.Max, res.Note)
		}
	}
	if got["живой оригинальный канал"] <= got["перезаливщик (одни ссылки)"] {
		t.Errorf("перезаливщик не ниже живого канала: %v", got)
	}
}

// Без данных компонент не считается и не тянет годноту вниз.
func TestSourceGoodnessMissingParts(t *testing.T) {
	res := SourceGoodness(GoodnessInput{LastPostAge: -1})
	if len(res.Parts) != 0 || res.Score != 0 {
		t.Errorf("пустые метрики: %+v", res)
	}
	rate := 0.3
	res = SourceGoodness(GoodnessInput{ViewRate: &rate, LastPostAge: -1})
	if _, ok := res.Parts["жизнь"]; ok || res.Parts["отклик"] != 100 {
		t.Errorf("только отклик: %+v", res)
	}
}

// Бюджет проверок: доля по виду — от «да» владельца, минимум 10 % каждому виду с кандидатами.
func TestDiscoveryBudget(t *testing.T) {
	kinds := map[string]int{"post_search": 30, "similar": 30, "model_handle": 5, "empty": 0}
	report := map[string]map[string]int{"post_search": {"found": 10, "approve": 8}, "similar": {"found": 10, "approve": 0}}
	caps := DiscoveryBudget(40, kinds, report)
	if _, ok := caps["empty"]; ok {
		t.Errorf("вид без кандидатов получил бюджет: %v", caps)
	}
	if caps["post_search"] <= caps["similar"] {
		t.Errorf("вид с большей долей «да» получил не больше: %v", caps)
	}
	for _, k := range []string{"similar", "model_handle"} {
		if caps[k] < 4 {
			t.Errorf("%s ниже минимума 10 %%: %v", k, caps)
		}
	}
	if sum := caps["post_search"] + caps["similar"] + caps["model_handle"]; sum < 38 || sum > 42 {
		t.Errorf("сумма бюджета %d, ждали около 40: %v", sum, caps)
	}
	if DiscoveryBudget(40, map[string]int{}, report) != nil && len(DiscoveryBudget(40, map[string]int{}, report)) != 0 {
		t.Errorf("пустая очередь — пустой бюджет")
	}
}
