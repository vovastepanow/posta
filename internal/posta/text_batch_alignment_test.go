package posta

import (
	"encoding/json"
	"os"
	"testing"

	"posta/internal/jx"
)

func TestRealSwappedTextBatchKeepsScoresWithTheirSources(t *testing.T) {
	raw, err := os.ReadFile("testdata/real_post_audit_cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []realPostAuditCase
	if err = json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	var texts []string
	for _, c := range cases {
		if c.Topic == "Криптовалюта" {
			texts = append(texts, c.Text)
		}
	}
	raw, err = os.ReadFile("testdata/text_batch_swapped_real.json")
	if err != nil {
		t.Fatal(err)
	}
	var response jx.M
	if err = json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	rows := alignTextBatchAnalysis(jx.Maps(response, "items"), texts)
	if len(rows) != 2 || jx.Int(rows[0], "relevance") != 10 || jx.Int(rows[1], "relevance") != 98 {
		t.Fatalf("scores still belong to the wrong posts: %v", rows)
	}
}

func TestTextBatchConflictsStayUnresolvedForIndividualRetry(t *testing.T) {
	row := jx.M{"index": 0, "relevance": 90, "advertising": false, "evidence": "кратко"}
	rows := alignTextBatchAnalysis([]jx.M{row, row}, []string{"первая запись", "вторая запись"})
	if rows[0] != nil || rows[1] != nil {
		t.Fatalf("conflicting batch must be retried, got %v", rows)
	}
	rows = alignTextBatchAnalysis([]jx.M{{"index": 1, "relevance": 90, "advertising": false}}, []string{"единственная запись"})
	if rows[0] == nil {
		t.Fatal("one-based singleton index lost the only possible post")
	}
}

func TestSpamFilterDoesNotBanTopicWordsAndScreenshots(t *testing.T) {
	for _, text := range []string{
		"Один мой знакомый решил начать жизнь с нуля. Огромную роль в этом сыграло казино.",
		"Новость об уязвимости в uTorrent: скриншот программы с настройками защиты.",
		"Разбор рисков memecoin и pump.fun без покупки токена.",
		"Что такое referral и реферальные программы: критический разбор.",
		"Screenshot of the application: инструкция по настройке.",
		"Почему промокод не применяется: независимый разбор интерфейса.",
	} {
		if IsSpamContent(text, text) {
			t.Errorf("ordinary content rejected as spam: %s", text)
		}
	}
	if !IsSpamContent("Пост #реклама", "Описание") || !IsSpamContent("", "") {
		t.Fatal("explicit advertising or empty material slipped through")
	}
}

func TestTopicRequirementsAreIndependentOfNumericThreshold(t *testing.T) {
	u := taxonomyTestUser()
	for _, topic := range []string{"Рецепты", "Кошки", "Кибербезопасность"} {
		raw := jx.M{"topic_match": false, "relevance": 95, "evidence": "Общее упоминание без обязательных признаков", "advertising": false, "format": "photo"}
		for _, an := range []Analysis{u.finishAnalysis(raw, topic, "", false, nil, false, "test"), u.finishTextAnalysis(raw, topic, "test")} {
			if jx.Int(an, "relevance") > 30 || u.QualityReasons(an, nil, topic, QualityOpts{Threshold: 20}) == "" {
				t.Fatalf("missing topic requirements passed a low numerical threshold: %v", an)
			}
		}
	}
	an := u.finishTextAnalysis(jx.M{"topic_match": true, "relevance": 90, "evidence": "Описание конкретной темы", "format": "photo"}, "Кошки", "actual-fallback")
	if jx.Str(an, "format") != "text" || jx.Str(an, "model") != "actual-fallback" {
		t.Fatalf("text-only analysis invented media or lost the actual model: %v", an)
	}
}

func TestCaptionAloneCannotRejectRealVideoCandidates(t *testing.T) {
	raw, err := os.ReadFile("testdata/real_video_candidates.json")
	if err != nil {
		t.Fatal(err)
	}
	var videos []jx.M
	if err = json.Unmarshal(raw, &videos); err != nil {
		t.Fatal(err)
	}
	for _, video := range videos {
		if captionVerdictCanReject(video, "text") || captionVerdictCanReject(video, "both") {
			t.Fatalf("unseen video rejected by its caption: %v", video["url"])
		}
	}
	if !captionVerdictCanReject(jx.M{"text": "Полный текст поста", "text_post": true}, "text") {
		t.Fatal("complete text-only evidence must remain usable")
	}
}
