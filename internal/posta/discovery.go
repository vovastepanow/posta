package posta

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
)

// Shared source discovery and review helpers. The retired manual scanner
// cannot launch a second admission pipeline. The public autopilot owns discovery.

var discoveryQueryStopwords = []string{"канал", "каналы", "телеграм", "телеграмм", "telegram", "tg", "тг", "тгк", "паблик", "паблики",
	"группа", "группы", "сообщество", "вк", "vk", "channel", "channels", "скачать", "смотреть", "онлайн", "бесплатно"}

var topicAdultWords = []string{"18+", "nsfw", "порно", "порн", "эрот", "хентай", "секс", "фап", "голые", "обнажен", "nude", "porn", "hentai", "фетиш"}

// DiscoveryCandidate — кандидат на проверку.
type DiscoveryCandidate struct {
	Platform, ID, Origin, OriginKind string
	Info                             jx.M
	Depth                            int
	Deferred, Retry                  bool
	Priority, Tail                   int
	Front                            bool
}

// ErrDiscoveryMissing — кандидата нет на площадке: отказ без повтора.
var ErrDiscoveryMissing = errors.New("кандидата нет")

// TopicOwnSourcesCount — своих источников у темы: каналы и паблики, привязанные к ней, и доски, совпавшие по тегам.
func (u *U) TopicOwnSourcesCount(d *SourceDB, topic string) int {
	if u.TopicSame(config.DefaultTopic, topic) {
		return math.MaxInt32
	}
	n := 0
	n += len(u.approvedImageboardThreads(d, topic))
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if len(jx.List(e, "topics")) > 0 && u.CatalogEntryMatchesTopic(e, topic, true) {
				n++
			}
		}
	}
	return n
}

func dvachTagBoards() []string {
	var out []string
	for b := range dvachBoardTags {
		out = append(out, b)
	}
	sort.Strings(out)
	return out
}

func (u *U) TopicLacksSources(d *SourceDB, topic string) bool {
	return u.TopicOwnSourcesCount(d, topic) < config.DiscoveryMinOwnSources
}

// TopicUsableSources — свои источники темы плюс высокие предложения автоподбора.
func (u *U) TopicUsableSources(d *SourceDB, topic string) int {
	if u.TopicSame(config.DefaultTopic, topic) {
		return math.MaxInt32
	}
	n := 0
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if len(jx.List(e, "topics")) > 0 && u.CatalogEntryMatchesTopic(e, topic, true) {
				n++
			}
		}
	}
	for _, s := range d.Suggestions {
		if u.TopicSame(jx.Str(s, "topic"), topic) && int(jx.Int(s, "score")) >= u.TInt("suggest_score") {
			n++
		}
	}
	return n
}

// discoveryProgress persists the unfinished batch and its unexamined candidates per topic.
type discoveryProgress struct {
	Platform     string                         `json:"platform,omitempty"`
	Target       int                            `json:"target"`
	Found        int                            `json:"found"`
	Pending      []DiscoveryCandidate           `json:"pending"`
	Active       bool                           `json:"active"`
	Checked      int                            `json:"checked"`
	Rejected     int                            `json:"rejected"`
	Failed       int                            `json:"failed"`
	Added        int                            `json:"added"`
	Suggested    int                            `json:"suggested"`
	Queries      map[string]discoveryQueryStats `json:"queries,omitempty"`
	ExtraQueries []string                       `json:"extra_queries,omitempty"`
	SearchRounds int                            `json:"search_rounds,omitempty"`
	ExpandedAt   int64                          `json:"expanded_at,omitempty"`
	Retries      []discoveryRetry               `json:"retries,omitempty"`
	Errors       []string                       `json:"errors,omitempty"`
}

func (u *U) discoveryProgressKey(topic string) string {
	if job, ok := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); ok && job != nil {
		return "discovery_progress:v2:" + repeatDigest(job.Scope+"\x00"+autopilotTopicKey(topic))
	}
	return "discovery_progress:" + sha1Short(TopicNorm(topic), 40)
}

func (u *U) discoveryScopedKey(key string) string {
	if job, ok := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); ok && job != nil {
		return key + ":v2:" + repeatDigest(job.Scope)
	}
	return key
}

func (u *U) discoveryScopedTopic(topic string) string {
	if job, ok := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); ok && job != nil {
		return autopilotTopicKey(topic)
	}
	return TopicNorm(topic)
}

// DiscoveryPipelinePublic combines persistent batch progress with the live state for this topic.
func (u *U) DiscoveryPipelinePublic(topic string) jx.M {
	p := KV(u.App, u.Ctx, u.discoveryProgressKey(topic), discoveryProgress{})
	verdicts := u.TopicVerdictStats(topic)
	passed, analyzed := 0, 0
	for _, counts := range verdicts {
		passed += counts[0]
		analyzed += counts[1]
	}
	ownSources := 0
	if e := u.TopicLookup(topic); e != nil {
		ownSources = u.TopicOwnSourceCount(e, nil)
	}
	out := jx.M{"platform": p.Platform, "enabled": u.TBool("discovery"), "active": p.Active, "found": p.Found, "target": p.Target,
		"pending": len(p.Pending), "checked": p.Checked, "rejected": p.Rejected, "failed": p.Failed,
		"added": p.Added, "suggested": p.Suggested, "running": false, "paused": false, "errors": p.Errors, "retry_count": len(p.Retries), "last_search": discoveryLastSearch(p.Queries), "insight": discoveryQueryInsight(p.Queries),
		"own_sources": ownSources, "analyzed_posts": analyzed, "accepted_posts": passed, "analyzed_sources": len(verdicts)}
	return out
}

// TopicHealthPublic is the observable funnel for one topic. Discovery and
// analysis counts are shared; pool and outcome counts belong to this user.
func (u *U) TopicHealthPublic(topic string) jx.M {
	d := u.DiscoveryPipelinePublic(topic)
	out := jx.M{"sources": d["own_sources"], "candidates": d["found"], "sources_checked": d["checked"],
		"analyzed": d["analyzed_posts"], "relevant": d["accepted_posts"], "pool": u.PoolTopicCount(topic),
		"liked": 0, "disliked": 0, "published": 0}
	if u.ID == "" {
		return out
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT collection, COALESCE(data->>'topic','') FROM history_items WHERE user_id=$1 AND collection IN ('liked','disliked')`, u.ID)
	if err == nil {
		for rows.Next() {
			var collection, savedTopic string
			if rows.Scan(&collection, &savedTopic) == nil && u.TopicSame(savedTopic, topic) {
				out[collection] = jx.Int(out, collection) + 1
			}
		}
		rows.Close()
	}
	rows, err = u.DB.Pool.Query(u.Ctx, `SELECT COALESCE(data->>'topic','') FROM published WHERE user_id=$1 AND data->>'status'='ok'`, u.ID)
	if err == nil {
		for rows.Next() {
			var savedTopic string
			if rows.Scan(&savedTopic) == nil && u.TopicSame(savedTopic, topic) {
				out["published"] = jx.Int(out, "published") + 1
			}
		}
		rows.Close()
	}
	return out
}

// discoveryTopicKnown — уже известные источники по теме.
func (u *U) discoveryTopicKnown(d *SourceDB, topic string) map[string]bool {
	known := map[string]bool{}
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if u.CatalogEntryMatchesTopic(e, topic, true) {
				known[SourceIDFor(c.Platform, jx.Str(e, "id"))] = true
			}
		}
	}
	for _, v := range d.Votes {
		t := jx.Str(v, "topic")
		if t == "" {
			t = config.DefaultTopic
		}
		if u.TopicSame(t, topic) {
			known[jx.Str(v, "source_id")] = true
		}
	}
	for _, s := range d.Suggestions {
		t := jx.Str(s, "topic")
		if t == "" {
			t = config.DefaultTopic
		}
		if u.TopicSame(t, topic) {
			known[jx.Str(s, "source_id")] = true
		}
	}
	return known
}

var cleanQuerySplitRe = regexp.MustCompile(`[\s,;:!?.()/|]+`)
var letterAnyRe = regexp.MustCompile(`\p{L}`)

// DiscoveryCleanQuery — запрос 1–4 слова без служебных слов; "" — не годится.
func DiscoveryCleanQuery(q string) string {
	q = strings.NewReplacer("«", " ", "»", " ", `"`, " ", "#", " ", "@", " ").Replace(q)
	var words []string
	for _, w := range cleanQuerySplitRe.Split(strings.ToLower(strings.TrimSpace(q)), -1) {
		if w != "" && !contains(discoveryQueryStopwords, w) {
			words = append(words, w)
		}
	}
	if len(words) == 0 || len(words) > 4 || !letterAnyRe.MatchString(strings.Join(words, "")) {
		return ""
	}
	return trimRunes(strings.Join(words, " "), 50)
}

type discoveryPlan struct {
	Queries  []string
	Telegram []string
}

// discoveryAIPlan — запросы: название темы, синонимы, готовые запросы дерева, запросы профиля, по два у частных тем.
func (u *U) discoveryAIPlan(topic string) discoveryPlan {
	profile := u.TopicProfileEnsure(topic)
	if profile == nil {
		u.Log("-> Профиль темы не составлен — поиск только по названию и его синонимам.")
	}
	e := u.TopicLookup(topic)
	var cands []string
	if e != nil {
		cands = append(append(append(cands, e.Title), e.Queries...), e.Aliases...)
	} else {
		cands = []string{topic}
	}
	if profile != nil {
		cands = append(cands, profile.Queries...)
	}
	if e != nil {
		for _, c := range u.TopicChildren(e) {
			if c.Profile != nil {
				cands = append(cands, c.Profile.Queries[:min(2, len(c.Profile.Queries))]...)
			}
		}
	}
	var queries []string
	seen := map[string]bool{}
	for _, q := range cands {
		if q = DiscoveryCleanQuery(q); q != "" && !TextIsUkrainian(q) && !seen[TopicNorm(q)] {
			seen[TopicNorm(q)] = true
			queries = append(queries, q)
		}
	}
	plan := discoveryPlan{Queries: queries[:min(12, len(queries))]}
	if profile != nil {
		plan.Telegram = profile.Telegram[:min(20, len(profile.Telegram))]
	}
	return plan
}

// DiscoveryTopicKeywords — слова темы из профиля.
func (a *App) DiscoveryTopicKeywords(topic string) []string {
	if e := a.TopicLookup(topic); e != nil && e.Profile != nil {
		return e.Profile.Keywords
	}
	return nil
}

func sourceQueryRule(topic string) string {
	q := TopicQueryOf(topic)
	if q == "" {
		return ""
	}
	return " Уточнение: «" + q + "». Канал должен быть не только по теме, но и про уточнение (место, команда, персонаж, бренд); общий канал по теме без уточнения — не подходит."
}

type sourceReview struct {
	Samples      int
	Score        int
	Reason       string
	PersonalBlog string
}

const sourceFormatPrompt = "\nДополнительно определи тип ИСТОЧНИКА, независимо от темы: поле personal_blog = yes, когда по нескольким публикациям ясно, что это личный блог одного постоянного автора/ведущего (в том числе появляется перед камерой); no, когда это медиа, бренд, группа авторов или безличная подборка; unknown, когда надёжных признаков недостаточно. Не делай вывод по одному лицу или одному посту. Верни это поле в JSON."

func (u *U) sourceReviewRequest(prompt, topic string, images [][]byte) *sourceReview {
	prompt += sourceFormatPrompt
	schema := jx.M{"type": "object", "properties": jx.M{"score": jx.M{"type": "integer", "minimum": 0, "maximum": 100}, "reason": jx.M{"type": "string"},
		"personal_blog": jx.M{"type": "string", "enum": []string{"yes", "no", "unknown"}}},
		"required": []string{"score", "reason"}}
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Images: images, Schema: schema, MaxTokens: 120, Temperature: 0, UserID: u.ID, Topic: topic})
	if err != nil {
		autopilotNoteAssessmentCause(u.Ctx, autopilotAssessmentCause(err))
		return nil
	}
	a := llm.DecodeJSON(r.Text)
	if a == nil || !validModelPercent(a["score"]) {
		autopilotNoteAssessmentCause(u.Ctx, "invalid_model_response")
		return nil
	}
	return &sourceReview{Score: llm.ClampScore(jx.Float(a, "score")), Reason: trimRunes(llm.StripCJK(jx.Str(a, "reason")), 80), PersonalBlog: personalBlogLabelFromReview(jx.Str(a, "personal_blog"))}
}

const notShop = " Не отклоняй источник только потому, что он принадлежит бренду, компании, агентству, магазину или автору платных услуг. Оцени фактическую тематику публикаций. Каталог товаров, объявления и самореклама без содержательной пользы не являются тематическим материалом. Независимо от тематической оценки реклама и качество отдельно проверяются на уровне публикаций."

// A 160-rune prefix often hides the actual takeaway of long posts. Retain
// both the opening and conclusion in a bounded snippet; disclose omission
// instead of silently presenting a truncated caption as the full post.
func sourceReviewCaption(text string) string {
	text = strings.TrimSpace(text)
	const limit = 850
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	head := strings.TrimSpace(string(runes[:570]))
	if cut := strings.LastIndexAny(head, " \n\t"); cut >= 450 {
		head = strings.TrimSpace(head[:cut])
	}
	tail := strings.TrimSpace(string(runes[len(runes)-250:]))
	if cut := strings.IndexAny(tail, " \n\t"); cut > 0 && cut <= 50 {
		tail = strings.TrimSpace(tail[cut:])
	}
	return head + " […] " + tail
}

// Keep SMM distinct from the neighboring Marketing, Legal, Management and
// Technology taxonomy subjects. A brand or channel title does not prove
// topical value; assess the actual mix of recent posts. Avoid broadening SMM
// to every corporate marketing publication while preserving useful social
// media advertising and content strategy publishers.
func sourceReviewTopicGuidance(topic string) string {
	if autopilotCanonicalTopic(topic) != "SMM и блогинг" {
		return ""
	}
	return `Дополнительные правила для темы «SMM и блогинг».
Подходят практические публикации о ведении сообществ и каналов, создании и стратегии контента, росте подписчиков, охватах и вовлечённости, таргетированной рекламе ВКонтакте/Telegram/других социальных платформ, работе с инфлюенсерами, аналитике конкретных кампаний в соцсетях, монетизации блогов и кейсах продвижения авторов или сообществ.
Не путай тему с соседним «Маркетинг и реклама»: контекстная реклама только в поисковых системах, общая экономика рекламного рынка, брендинг без соцсетей, абстрактный бизнес-менеджмент и продажи сами по себе НЕ являются SMM. Также не подходят юридические консультации, HR, программирование и общие новости технологий, если их посты не раскрывают SMM.
Оцени распределение тем по НЕСКОЛЬКИМ показанным публикациям, а не только название источника, профессию владельца или один удачный пост. Если часть публикаций профессионально полезна для SMM, а остальные не по теме, укажи смешанный характер в причине и не давай уверенно высокий балл. Новости мероприятия или продажа услуг без практического содержания не равны профессиональному разбору. Не принимай решение по одному слову «маркетинг» или «реклама». `
}

func (u *U) sourceTextReview(title, desc, topic string, texts []string) *sourceReview {
	var t []string
	seen := map[string]bool{}
	for _, x := range texts {
		x = sourceReviewCaption(x)
		if x != "" && x != EmptyPostText && !seen[x] && len(t) < config.SourceReviewCaptions {
			seen[x] = true
			t = append(t, x)
		}
	}
	if len(t) == 0 {
		return nil
	}
	prompt := u.prompt("source_text", map[string]string{"topic": u.TopicPromptContext(topic) + "." + u.TopicProfileHint(topic), "title": title, "description": trimRunes(desc, 400), "captions": strings.Join(t, "»; «"), "query_rule": sourceQueryRule(topic), "shop_rule": notShop})
	prompt += sourceReviewTopicGuidance(topic)
	review := u.sourceReviewRequest(prompt, topic, nil)
	if review != nil {
		review.Samples = len(t)
	}
	return review
}

func (u *U) sourceImagesReview(title, topic string, posts []jx.M) *sourceReview {
	var frames [][]byte
	for _, p := range posts {
		if len(frames) >= config.StoryboardFramesPerSheet {
			break
		}
		if b := u.discoveryImageBytes(jx.Str(p, "image")); b != nil {
			frames = append(frames, b)
		}
	}
	if len(frames) == 0 {
		return nil
	}
	prompt := u.prompt("source_images", map[string]string{"topic": u.TopicPromptContext(topic) + "." + u.TopicProfileHint(topic), "title": title, "query_rule": sourceQueryRule(topic), "shop_rule": notShop})
	prompt += sourceReviewTopicGuidance(topic)
	review := u.sourceReviewRequest(prompt, topic, StoryboardSheets(frames))
	if review != nil {
		review.Samples = len(frames)
	}
	return review
}

// A single multimodal request grades text and images independently and, when
// examples exist, estimates owner taste without another model round trip.
func (u *U) combinedSourceReview(title, desc, topic string, posts []jx.M, caps []string) *sourceEvaluation {
	var frames [][]byte
	for _, p := range posts {
		if len(frames) >= config.StoryboardFramesPerSheet {
			break
		}
		if b := u.discoveryImageBytes(jx.Str(p, "image")); b != nil {
			frames = append(frames, b)
		}
	}
	if len(frames) == 0 {
		u.Log("-> Объединённая оценка: картинки недоступны.")
		return nil
	}
	var texts []string
	for _, cap := range caps {
		if len(texts) >= config.SourceReviewCaptions {
			break
		}
		texts = append(texts, sourceReviewCaption(cap))
	}
	prompt := u.prompt("source_combined", map[string]string{"topic": u.TopicPromptContext(topic) + "." + u.TopicProfileHint(topic), "title": title, "description": trimRunes(desc, 400), "query_rule": sourceQueryRule(topic), "captions": strings.Join(texts, "»; «"), "shop_rule": notShop}) + sourceFormatPrompt + sourceReviewTopicGuidance(topic)
	props := jx.M{"text_score": jx.M{"type": "integer", "minimum": 0, "maximum": 100}, "image_score": jx.M{"type": "integer", "minimum": 0, "maximum": 100}, "reason": jx.M{"type": "string"}, "personal_blog": jx.M{"type": "string", "enum": []string{"yes", "no", "unknown"}}}
	required := []string{"text_score", "image_score", "reason"}
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Images: StoryboardSheets(frames), Schema: jx.M{"type": "object", "properties": props, "required": required}, MaxTokens: 180, Temperature: 0, UserID: u.ID, Topic: topic})
	if err != nil {
		autopilotNoteAssessmentCause(u.Ctx, autopilotAssessmentCause(err))
		u.Log("-> Объединённая оценка: ошибка модели: %v", err)
		return nil
	}
	ev := parseCombinedSourceReview(r.Text, false)
	if ev == nil {
		autopilotNoteAssessmentCause(u.Ctx, "invalid_model_response")
		u.Log("-> Объединённая оценка: модель не вернула все оценки; использую обычную проверку.")
	} else {
		ev.ReviewedSamples = min(len(texts), len(frames))
	}
	return ev
}

func parseCombinedSourceReview(reply string, hasTaste bool) *sourceEvaluation {
	data := llm.DecodeJSON(reply)
	if data == nil || !validModelPercent(data["text_score"]) || !validModelPercent(data["image_score"]) || (hasTaste && !validModelPercent(data["taste_score"])) {
		return nil
	}
	textScore := llm.ClampScore(jx.Float(data, "text_score"))
	imageScore := llm.ClampScore(jx.Float(data, "image_score"))
	ev := &sourceEvaluation{Score: min(textScore, imageScore), Text: &textScore, Images: []int{imageScore}, Reason: trimRunes(llm.StripCJK(jx.Str(data, "reason")), 80), PersonalBlog: personalBlogLabelFromReview(jx.Str(data, "personal_blog")), TasteIncluded: true}
	if hasTaste {
		taste := llm.ClampScore(jx.Float(data, "taste_score"))
		ev.Taste = &taste
	}
	return ev
}

type sourceEvaluation struct {
	ReviewedSamples int
	PersonalBlog    string
	Score           int
	Images          []int
	Text            *int
	TextOnly        bool
	Reason          string
	Quality         jx.M
	Goodness        *GoodnessResult
	Taste           *int
	TasteIncluded   bool
}

// sourcePostsHaveImages checks the presence of media references without fetching
// or decoding them. Missing pictures should not block text-only feed reviews.
func sourcePostsHaveImages(posts []jx.M) bool {
	for _, post := range posts {
		if strings.TrimSpace(jx.Str(post, "image")) != "" {
			return true
		}
	}
	return false
}

// evaluateSource — подписи есть — проверка по тексту; нет — коллаж картинок (по формату темы).
func (u *U) evaluateSource(info jx.M, posts []jx.M, topic string) *sourceEvaluation {
	title, desc := jx.Str(info, "title"), jx.Str(info, "description")
	var caps []string
	for _, p := range posts {
		if t := strings.TrimSpace(jx.Str(p, "text")); t != "" && t != EmptyPostText {
			caps = append(caps, t)
		}
	}
	format := u.TopicFormatOf(topic)
	judge := format.Judge
	if len(caps) > 0 && judge != "image" && judge != "text" {
		if combined := u.combinedSourceReview(title, desc, topic, posts, caps); combined != nil {
			return combined
		}
		if u.Ctx.Err() != nil {
			return nil
		}
	}
	var text *sourceReview
	if len(caps) > 0 && judge != "image" {
		text = u.sourceTextReview(title, desc, topic, caps)
	}
	if text != nil && judge == "text" {
		s := text.Score
		return &sourceEvaluation{Score: s, Text: &s, TextOnly: s < config.DiscoveryTextRejectScore, Reason: text.Reason, PersonalBlog: text.PersonalBlog, ReviewedSamples: text.Samples}
	}
	// Text-only feeds have no storyboard to score. An available text review
	// is sufficient for non-image topics; do not turn missing media into an
	// unavailable classifier result. Image-required topics remain strict.
	if text != nil && judge != "image" && !sourcePostsHaveImages(posts) {
		s := text.Score
		return &sourceEvaluation{Score: s, Text: &s, TextOnly: s < config.DiscoveryTextRejectScore, Reason: text.Reason, PersonalBlog: text.PersonalBlog, ReviewedSamples: text.Samples}
	}
	review := u.sourceImagesReview(title, topic, posts)
	if review == nil {
		// Mixed topics such as SMM explicitly accept text-only posts. Many
		// public feeds advertise an image URL but prohibit hotlinking the
		// image itself. That is not evidence that a successful TEXT review
		// was invalid. Previously the mere presence of the URL forced an
		// image assessment and discarded a valid text score, keeping the
		// source stuck in assessment_unavailable forever.
		//
		// Image-required topics remain strict. An accepted source still
		// needs post-level evidence and the normal probation thresholds;
		// this path never activates it directly.
		if text != nil && format.Media == "optional" && judge != "image" {
			score := text.Score
			return &sourceEvaluation{Score: score, Text: &score, TextOnly: true, Reason: text.Reason, PersonalBlog: text.PersonalBlog, ReviewedSamples: text.Samples}
		}
		return nil
	}
	if text == nil {
		return &sourceEvaluation{Score: review.Score, Images: []int{review.Score}, Reason: review.Reason, PersonalBlog: review.PersonalBlog, ReviewedSamples: review.Samples}
	}
	s := text.Score
	return &sourceEvaluation{Score: min(text.Score, review.Score), Images: []int{review.Score}, Text: &s, Reason: review.Reason, PersonalBlog: review.PersonalBlog, ReviewedSamples: min(text.Samples, review.Samples)}
}

func (a *App) dvachBoardList() map[string]jx.M {
	var list []jx.M
	out := map[string]jx.M{}
	if !a.HTTPGetJSON("https://2ch.hk/api/mobile/v2/boards", 10*time.Second, &list) {
		return out
	}
	for _, b := range list {
		id := jx.Str(b, "id")
		if IsValidDvachBoard(id) {
			out[id] = jx.M{"id": id, "name": strings.TrimSpace(jx.Str(b, "name")), "category": strings.TrimSpace(jx.Str(b, "category"))}
		}
	}
	return out
}

var tagSplitRe = regexp.MustCompile(`[\s-]+`)
var twoCharRe = regexp.MustCompile(`^[a-z0-9+]{2}$`)

func (a *App) DvachTagWords(text string) []string { return dvachTagWords(text) }

func dvachTagWords(text string) []string {
	var out []string
	for _, w := range tagSplitRe.Split(TopicNorm(text), -1) {
		if runeLen(w) >= 3 || twoCharRe.MatchString(w) {
			out = append(out, w)
		}
	}
	return out
}

// dvachWordsMatch — то же слово с другим окончанием: общее начало ≥ 4 букв, отличие ≤ 2–3 букв.
func dvachWordsMatch(a, b string) bool {
	if a == b {
		return true
	}
	ra, rb := []rune(a), []rune(b)
	common := 0
	for common < min(len(ra), len(rb)) && ra[common] == rb[common] {
		common++
	}
	return common >= 4 && common >= min(len(ra), len(rb))-2 && common >= max(len(ra), len(rb))-3
}

func DvachTagMatches(tag string, topicWords []string) bool {
	words := dvachTagWords(tag)
	if len(words) == 0 {
		return false
	}
	for _, tw := range words {
		ok := false
		for _, w := range topicWords {
			if dvachWordsMatch(tw, w) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	return true
}

// TopicIsAdult — тема по словам 18+.
func TopicIsAdult(topic string) bool { return topicIsAdultIn(nil, topic) }

func topicIsAdultIn(a *App, topic string) bool {
	parts := []string{topic}
	if a != nil {
		if e := a.TopicLookup(topic); e != nil {
			parts = append([]string{e.Title, e.About}, e.Aliases...)
		}
	}
	text := " " + TopicNorm(strings.Join(parts, " ")) + " "
	for _, w := range topicAdultWords {
		if strings.Contains(text, " "+w) || strings.Contains(text, w+" ") {
			return true
		}
	}
	return false
}

// DvachBoardsByTags — доски, теги которых совпали с темой: слова темы втрое, слова общей темы — раз, первый тег доски — вдвое.
func (u *U) DvachBoardsByTags(topic string, boards []string) map[string]int {
	e := u.TopicLookup(topic)
	parts := []string{topic}
	if e != nil {
		parts = append([]string{e.Title}, e.Aliases...)
	}
	own := dvachTagWords(strings.Join(parts, " "))
	var parent []string
	if e != nil {
		if p := u.TopicParent(e); p != nil {
			parent = dvachTagWords(p.Title)
		}
	}
	adult := topicIsAdultIn(u.App, topic) || u.AdultAllowed()
	scores := map[string]int{}
	for _, b := range boards {
		tags := DvachBoardTags(b)
		if contains(tags, DvachAdultTag) && !adult {
			continue
		}
		score, i := 0, 0
		for _, t := range tags {
			if t == DvachAdultTag {
				continue
			}
			w := 1
			if i == 0 {
				w = 2
			}
			i++
			if DvachTagMatches(t, own) {
				score += 3 * w
			} else if len(parent) > 0 && DvachTagMatches(t, parent) {
				score += w
			}
		}
		if score > 0 {
			scores[b] = score
		}
	}
	return scores
}

func sortedByScore(m map[string]int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	return keys
}

func (u *U) chooseDvachBoards(topic string, boards map[string]jx.M) ([]string, bool) {
	if len(boards) == 0 {
		return []string{}, true
	}
	adult := topicIsAdultIn(u.App, topic) || u.AdultAllowed()
	allowed := map[string]jx.M{}
	var ids []string
	for id, b := range boards {
		if adult || !contains(DvachBoardTags(id), DvachAdultTag) {
			allowed[id] = b
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	byTags := sortedByScore(u.DvachBoardsByTags(topic, ids))
	if len(byTags) > 0 {
		u.Log("-> По тегам подходят: /%s/.", strings.Join(byTags[:min(8, len(byTags))], "/, /"))
	}
	chosen := byTags[:min(config.DvachBoardsPerTopic, len(byTags))]
	if len(chosen) >= config.DvachBoardsPerTopic {
		return chosen, true
	}
	var lines []string
	for _, id := range ids {
		b := allowed[id]
		line := id + " — " + jx.Str(b, "name")
		var tags []string
		for _, t := range DvachBoardTags(id) {
			if t != DvachAdultTag {
				tags = append(tags, t)
			}
		}
		if len(tags) > 0 {
			line += ": " + strings.Join(tags, ", ")
		} else if c := jx.Str(b, "category"); c != "" {
			line += " (" + c + ")"
		}
		lines = append(lines, line)
	}
	prompt := u.prompt("dvach_boards", map[string]string{"topic": u.TopicPromptContext(topic), "boards": strings.Join(lines, "\n"), "limit": fmt.Sprint(config.DvachBoardsPerTopic)})
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleText, Prompt: prompt, JSON: true, MaxTokens: 80, Temperature: 0, UserID: u.ID})
	if err != nil {
		u.Log("-> Нейросеть не выбрала доски Двача: %s", err)
		return chosen, len(chosen) > 0
	}
	for _, b := range jx.Strs(llm.DecodeJSON(r.Text), "boards") {
		b = strings.ToLower(strings.Trim(b, " /\t"))
		if _, ok := allowed[b]; ok && !contains(chosen, b) {
			chosen = append(chosen, b)
		}
	}
	return chosen[:min(len(chosen), config.DvachBoardsPerTopic)], true
}

// DvachBoardsForTopic — доски темы: у темы по умолчанию — общий список, у других выбираются один раз и запоминаются.
func (u *U) DvachBoardsForTopic(topic string, chooseIfMissing bool) []string {
	d := u.Catalog()
	if u.TopicSame(config.DefaultTopic, topic) {
		return d.Boards
	}
	key := strings.ToLower(topic)
	saved, has := d.DvachTopicBoards[key]
	always := func(l []string) []string {
		return uniqueStrings(append(append([]string{}, l...), config.DvachAlwaysBoards...))
	}
	tagged := contains(jx.Strs(d.Discovery, "dvach_tagged"), key)
	if !chooseIfMissing || (has && tagged) {
		return always(saved)
	}
	u.Log("Подбираю доски Двача под тему «%s»...", topic)
	list := u.dvachBoardList()
	only := map[string]jx.M{}
	for id, b := range list {
		if _, ok := dvachBoardTags[id]; ok {
			only[id] = b
		}
	}
	chosen, ok := u.chooseDvachBoards(topic, only)
	if !ok {
		return always(saved)
	}
	chosen = uniqueStrings(append(chosen, saved...))
	u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		d.DvachTopicBoards[key] = chosen
		d.Discovery["dvach_tagged"] = uniqueStrings(append(jx.Strs(d.Discovery, "dvach_tagged"), key))
		return true
	})
	if len(chosen) > 0 {
		u.Log("-> Доски Двача для темы: /%s/.", strings.Join(chosen, "/, /"))
	} else {
		u.Log("-> Доски Двача для темы: подходящих нет.")
	}
	return always(chosen)
}

func linkedCandidates(info jx.M) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	switch l := info["linked"].(type) {
	case []DiscoveryCandidate:
		return l
	case []any:
		for _, x := range l {
			if m, ok := x.(jx.M); ok {
				out = append(out, DiscoveryCandidate{Platform: jx.Str(m, "platform"), ID: jx.Str(m, "id")})
			}
		}
	}
	return out
}

// discoveryTelegramAccountChannels — каналы через аккаунт Telegram: поиск по запросам или похожие.
func (u *U) discoveryTelegramAccountChannels(queries, similarTo []string) []TGChannel {
	if !u.TelegramUserConnected() {
		return nil
	}
	var observe func(string, int, error)
	if u.discoverySearchObservation != nil {
		observe = func(q string, count int, err error) {
			status := "ok"
			if err != nil {
				status = "unavailable"
			} else if count == 0 {
				status = "empty"
			}
			u.discoverySearchObservation("telegram", q, status)
		}
	}
	chs, err := u.telegramUserFindChannelsObserved(queries, similarTo, observe)
	if err != nil {
		u.Log("-> Поиск через аккаунт Telegram: %s", err)
		return nil
	}
	var out []TGChannel
	min := int64(u.TInt("min_subscribers"))
	for _, c := range chs {
		if c.Subscribers >= min {
			out = append(out, c)
		}
	}
	return out
}

var pipePrefixRe = regexp.MustCompile(`^[^|]{1,24}\|\s*`)

func discoveryPlainName(n string) string {
	return strings.TrimSpace(pipePrefixRe.ReplaceAllString(strings.TrimSpace(n), ""))
}

func discoveryCleanHandles(handles []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, h := range handles {
		h = strings.TrimLeft(strings.TrimSpace(tmeRe.ReplaceAllString(h, "")), "@")
		if IsValidTelegramChannel(h) && !seen[strings.ToLower(h)] {
			seen[strings.ToLower(h)] = true
			out = append(out, h)
		}
	}
	return out[:min(len(out), 15)]
}

func (u *U) discoveryApprovedNames(d *SourceDB, topic string) []string {
	var names []string
	for _, k := range []string{"telegram_channels", "vk_publics", "rss_feeds"} {
		l := *d.List(k)
		for i := len(l) - 1; i >= 0; i-- {
			if u.CatalogEntryMatchesTopic(l[i], topic, false) {
				names = append(names, discoveryPlainName(jx.Str(l[i], "name")))
			}
		}
	}
	var votes []jx.M
	for _, v := range d.Votes {
		if jx.Str(v, "vote") == "approve" && u.TopicSame(jx.Str(v, "topic"), topic) {
			votes = append(votes, v)
		}
	}
	sort.SliceStable(votes, func(i, j int) bool { return jx.Int(votes[i], "timestamp") > jx.Int(votes[j], "timestamp") })
	for _, v := range votes {
		names = append(names, discoveryPlainName(jx.Str(v, "name")))
	}
	var out []string
	for _, n := range uniqueStrings(names) {
		if n != "" {
			out = append(out, n)
		}
	}
	return out[:min(25, len(out))]
}

func (u *U) discoveryRejectedNames(votes []jx.M, topic string) []string {
	var rej []jx.M
	for _, v := range votes {
		if jx.Str(v, "vote") == "reject" && jx.Str(v, "reason") == "off_topic" && u.TopicSame(jx.Str(v, "topic"), topic) {
			rej = append(rej, v)
		}
	}
	sort.SliceStable(rej, func(i, j int) bool { return jx.Int(rej[i], "timestamp") > jx.Int(rej[j], "timestamp") })
	var out []string
	for _, v := range rej {
		out = append(out, discoveryPlainName(jx.Str(v, "name")))
	}
	out = uniqueStrings(out)
	return out[:min(15, len(out))]
}

// SourceVoteOrigin — происхождение решения для учёта по видам находок.
func SourceVoteOrigin(s jx.M) jx.M {
	if s == nil {
		return jx.M{"origin_kind": "catalog"}
	}
	o := jx.Map(s, "origin")
	kind := jx.Str(o, "kind")
	if kind == "" {
		kind = "unknown"
	}
	return jx.M{"origin_kind": kind, "origin_detail": jx.Str(o, "detail"), "discovery_score": jx.Int(s, "score")}
}

// DiscoveryOriginBonus — поправка к баллу предложения по виду находки: (доля «да» − 0,3) × 30, ±10.
func (a *App) DiscoveryOriginBonus(votes []jx.M, topic, kind string) float64 {
	if kind == "unknown" || kind == "other" {
		return 0
	}
	ofKind := func(list []jx.M) []jx.M {
		var out []jx.M
		for _, v := range list {
			if jx.Str(v, "origin_kind") == kind {
				out = append(out, v)
			}
		}
		return out
	}
	var own []jx.M
	for _, v := range votes {
		if a.TopicSame(jx.Str(v, "topic"), topic) {
			own = append(own, v)
		}
	}
	rows := ofKind(own)
	if len(rows) < config.DiscoveryOriginMinVotes {
		rows = ofKind(votes)
	}
	if len(rows) < config.DiscoveryOriginMinVotes {
		return 0
	}
	yes := 0
	for _, v := range rows {
		if jx.Str(v, "vote") == "approve" {
			yes++
		}
	}
	return math.Max(-10, math.Min(10, (float64(yes)/float64(len(rows))-0.3)*30))
}

func SuggestionRank(s jx.M, bonus float64) float64 {
	return jx.Float(s, "score") + float64(config.DiscoveryRefBonus*min(len(jx.List(s, "refs")), 4)) + bonus
}

func DiscoveryReasonWithRefs(reason string, refs []string) string {
	if len(refs) == 0 {
		return reason
	}
	return fmt.Sprintf("%s Ещё %d раз найден через %s.", reason, len(refs), strings.Join(refs[:min(3, len(refs))], ", "))
}

func (u *U) discoveryAddRefs(topic string, refs map[string][]string) {
	if len(refs) == 0 {
		return
	}
	u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		changed := false
		for _, s := range d.Suggestions {
			add := refs[jx.Str(s, "source_id")]
			if len(add) == 0 || !u.TopicSame(jx.Str(s, "topic"), topic) {
				continue
			}
			all := uniqueStrings(append(jx.Strs(s, "refs"), add...))
			s["refs"] = all[:min(len(all), config.DiscoveryRefsMax)]
			s["timestamp"] = now()
			changed = true
			u.Log("-> %s: повторно найден — записей происхождения: %d.", jx.Str(s, "name"), len(jx.List(s, "refs")))
		}
		return changed
	})
}

var DiscoveryOriginLabels = map[string]string{"tg_search": "поиск Telegram", "tg_name": "название от модели", "model_handle": "адрес от модели",
	"similar": "похожие", "mentioned": "упомянут в", "vk_search": "поиск VK", "youtube": "YouTube", "tiktok": "TikTok", "ok": "Одноклассники",
	"deferred": "отложенные", "catalog": "каталог", "other": "прочее", "unknown": "до учёта", "before": "до учёта", "post_search": "нашёлся по постам", "tg_posts": "поиск постов Telegram"}

// DiscoveryOriginReport — по виду находки: найдено, да / может быть / нет. Показывает, какой способ поиска работает.
func (a *App) DiscoveryOriginReport(d *SourceDB, topic string) map[string]map[string]int {
	rows := map[string]map[string]int{}
	bump := func(kind, field string) {
		if rows[kind] == nil {
			rows[kind] = map[string]int{"found": 0, "approve": 0, "maybe": 0, "reject": 0}
		}
		rows[kind][field]++
	}
	in := func(e jx.M) bool { return topic == "" || a.TopicSame(jx.Str(e, "topic"), topic) }
	for _, s := range d.Suggestions {
		if in(s) {
			k := jx.Str(jx.Map(s, "origin"), "kind")
			if k == "" {
				k = "before"
			}
			bump(k, "found")
		}
	}
	for _, v := range d.Votes {
		vote := jx.Str(v, "vote")
		if !in(v) || (vote != "approve" && vote != "maybe" && vote != "reject") {
			continue
		}
		k := jx.Str(v, "origin_kind")
		if k == "" || k == "unknown" {
			k = "before"
		}
		bump(k, "found")
		bump(k, vote)
	}
	return rows
}
