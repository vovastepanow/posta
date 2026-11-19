package posta

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
)

// Автоподбор источников по теме: поиск и «похожие каналы» Telegram через аккаунт, поиск постов, поиск VK, YouTube, TikTok, ОК,
// ссылки из каналов темы, каналы, которые знает умная модель (каждый проверяется). Проверка поэтапная: текст канала
// умной моделью, при необходимости — коллаж картинок, затем аудитория. Подошедшие — в предложения на вкладке «Источники».

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

func DiscoveryLastRun(d *SourceDB, topic string) int64 {
	return jx.Int(jx.Map(d.Discovery, "runs"), strings.ToLower(strings.TrimSpace(topic)))
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

func (u *U) DiscoveryProgress(topic string) (found, target, pending int, active bool) {
	p := KV(u.App, u.Ctx, u.discoveryProgressKey(topic), discoveryProgress{})
	for _, c := range p.Pending {
		if discoveryMatchesPlatform(c, p.Platform) {
			pending++
		}
	}
	return p.Found, p.Target, pending, p.Active
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
	u.parsers.mu.Lock()
	paused := u.parsers.discoveryPaused[TopicNorm(topic)]
	u.parsers.mu.Unlock()
	out := jx.M{"platform": p.Platform, "enabled": u.TBool("discovery"), "active": p.Active, "found": p.Found, "target": p.Target,
		"pending": len(p.Pending), "checked": p.Checked, "rejected": p.Rejected, "failed": p.Failed,
		"added": p.Added, "suggested": p.Suggested, "running": false, "paused": paused, "errors": p.Errors, "retry_count": len(p.Retries), "last_search": discoveryLastSearch(p.Queries), "insight": discoveryQueryInsight(p.Queries),
		"own_sources": ownSources, "analyzed_posts": analyzed, "accepted_posts": passed, "analyzed_sources": len(verdicts)}
	if value, ok := u.discoveryLive.Load(u.discoveryProgressKey(topic)); ok {
		for key, value := range value.(jx.M) {
			out[key] = value
		}
	}
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

// SourceDiscoveryDue never schedules the retired legacy source scanner.
// Active public demands are handled by the dedicated source-autopilot worker.
func (u *U) SourceDiscoveryDue(d *SourceDB, topic string) bool {
	return false
}

func (a *App) RequestSourceDiscovery() {
	a.UpdateCatalog(a.ctxOrBg(), func(d *SourceDB) bool { d.Discovery["requested"] = true; return true })
}

// discoveryKnownIDs — уже известные: каталог и реестр проверенных (пропуск по решению реестра, без сетевых запросов).
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

func (u *U) discoveryKnownIDs(d *SourceDB, topic string) map[string]bool {
	known := u.discoveryTopicKnown(d, topic)
	for sid := range u.registryKnown(topic) {
		known[sid] = true
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

// discoveryKeywordHit — слово темы в названии, описании или в нескольких последних подписях. nil — слов нет.
func (u *U) discoveryKeywordHit(topic, title, desc string, texts []string) *bool {
	words := u.DiscoveryTopicKeywords(topic)
	if len(words) == 0 {
		return nil
	}
	yes, no := true, false
	if TopicWordsHit(title, words) || TopicWordsHit(desc, words) {
		return &yes
	}
	hits, n := 0, 0
	for _, t := range texts {
		if strings.TrimSpace(t) == "" {
			continue
		}
		if n++; n > config.DiscoveryKeywordRecent {
			break
		}
		if TopicWordsHit(t, words) {
			hits++
		}
	}
	if hits >= config.DiscoveryKeywordMinTexts {
		return &yes
	}
	return &no
}

func (u *U) discoveryTitleHit(topic, title, desc string) *bool {
	words := u.DiscoveryTopicKeywords(topic)
	if len(words) == 0 {
		return nil
	}
	r := TopicWordsHit(title, words) || TopicWordsHit(desc, words)
	return &r
}

func sourceQueryRule(topic string) string {
	q := TopicQueryOf(topic)
	if q == "" {
		return ""
	}
	return " Уточнение: «" + q + "». Канал должен быть не только по теме, но и про уточнение (место, команда, персонаж, бренд); общий канал по теме без уточнения — не подходит."
}

// RunQueryDiscovery — подбор источников по уточнению: поиск каналов Telegram по запросу и по запросу со словом темы.
func (u *U) RunQueryDiscovery(key string) {
	q := TopicQueryOf(key)
	if q == "" {
		return
	}
	lock := u.namedLock("query:" + strings.ToLower(key))
	if !lock.TryLock() {
		return
	}
	defer lock.Unlock()
	base := TopicBase(key)
	queries := uniqueStrings([]string{q, q + " " + base})
	u.Log("-> Подбор источников по уточнению «%s»: ищу каналы в Telegram по запросам «%s».", q, strings.Join(queries, "», «"))
	found := u.discoveryTelegramAccountChannels(queries, nil)
	found = found[:min(len(found), config.TopicQueryChannelsPerRun)]
	u.Log("-> Найдено каналов: %d.", len(found))
	stats := map[string]int{}
	known := u.discoveryKnownIDs(u.Catalog(), key)
	for _, ch := range found {
		if sourceKnownWithLegacy(known, SourceIDFor(PlatformTelegram, ch.Username)) {
			continue
		}
		u.checkDiscoveryCandidate(DiscoveryCandidate{Platform: PlatformTelegram, ID: ch.Username, Origin: "поиск по уточнению «" + q + "»", OriginKind: "tg_name"}, key, stats)
	}
	u.Log("-> Итог по уточнению: проверено %d, принято %d, в предложения %d, отказ %d.", stats["checked"], stats["added"], stats["suggested"], stats["rejected"])
}

// QueryDiscoveryQuotaTake — не больше TopicQueryDiscoveryPerDay новых уточнений в сутки.
func (u *U) QueryDiscoveryQuotaTake(key string) bool {
	allowed := false
	k := strings.ToLower(key)
	KVUpdate(u.App, u.Ctx, "query_discovery:"+u.ID+":"+time.Now().Format("2006-01-02"), 48*time.Hour, func(list []string, _ bool) *[]string {
		if contains(list, k) {
			allowed = true
			return &list
		}
		if len(list) >= config.TopicQueryDiscoveryPerDay {
			return &list
		}
		allowed = true
		list = append(list, k)
		return &list
	})
	return allowed
}

type sourceReview struct {
	Score        int
	Reason       string
	PersonalBlog string
}

const sourceFormatPrompt = "\nДополнительно определи тип ИСТОЧНИКА, независимо от темы: поле personal_blog = yes, когда по нескольким публикациям ясно, что это личный блог одного постоянного автора/ведущего (в том числе появляется перед камерой); no, когда это медиа, бренд, группа авторов или безличная подборка; unknown, когда надёжных признаков недостаточно. Не делай вывод по одному лицу или одному посту. Верни это поле в JSON."

func (u *U) sourceReviewRequest(prompt string, images [][]byte) *sourceReview {
	prompt += sourceFormatPrompt
	schema := jx.M{"type": "object", "properties": jx.M{"score": jx.M{"type": "integer", "minimum": 0, "maximum": 100}, "reason": jx.M{"type": "string"},
		"personal_blog": jx.M{"type": "string", "enum": []string{"yes", "no", "unknown"}}},
		"required": []string{"score", "reason"}}
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Images: images, Schema: schema, MaxTokens: 120, Temperature: 0, UserID: u.ID})
	if err != nil {
		return nil
	}
	a := llm.DecodeJSON(r.Text)
	if a == nil || a["score"] == nil {
		return nil
	}
	return &sourceReview{Score: llm.ClampScore(jx.Float(a, "score")), Reason: trimRunes(llm.StripCJK(jx.Str(a, "reason")), 80), PersonalBlog: personalBlogLabelFromReview(jx.Str(a, "personal_blog"))}
}

const notShop = " Канал магазина, компании, сервиса или бренда, который продаёт товары или услуги, — не тема, даже если товар связан с темой."

func (u *U) sourceTextReview(title, desc, topic string, texts []string) *sourceReview {
	var t []string
	for _, x := range texts {
		x = trimRunes(strings.TrimSpace(x), 160)
		if x != "" && x != EmptyPostText && len(t) < config.SourceReviewCaptions {
			t = append(t, x)
		}
	}
	prompt := u.prompt("source_text", map[string]string{"topic": u.TopicPromptContext(topic) + "." + u.TopicProfileHint(topic), "title": title, "description": trimRunes(desc, 400), "captions": strings.Join(t, "»; «"), "query_rule": sourceQueryRule(topic), "shop_rule": notShop})
	return u.sourceReviewRequest(prompt, nil)
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
	return u.sourceReviewRequest(prompt, StoryboardSheets(frames))
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
		texts = append(texts, trimRunes(cap, 160))
	}
	prompt := u.prompt("source_combined", map[string]string{"topic": u.TopicPromptContext(topic) + "." + u.TopicProfileHint(topic), "title": title, "description": trimRunes(desc, 400), "query_rule": sourceQueryRule(topic), "captions": strings.Join(texts, "»; «"), "shop_rule": notShop}) + sourceFormatPrompt
	props := jx.M{"text_score": jx.M{"type": "integer", "minimum": 0, "maximum": 100}, "image_score": jx.M{"type": "integer", "minimum": 0, "maximum": 100}, "reason": jx.M{"type": "string"}, "personal_blog": jx.M{"type": "string", "enum": []string{"yes", "no", "unknown"}}}
	required := []string{"text_score", "image_score", "reason"}
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Images: StoryboardSheets(frames), Schema: jx.M{"type": "object", "properties": props, "required": required}, MaxTokens: 180, Temperature: 0, UserID: u.ID})
	if err != nil {
		u.Log("-> Объединённая оценка: ошибка модели: %v", err)
		return nil
	}
	ev := parseCombinedSourceReview(r.Text, false)
	if ev == nil {
		u.Log("-> Объединённая оценка: модель не вернула все оценки; использую обычную проверку.")
	}
	return ev
}

func parseCombinedSourceReview(reply string, hasTaste bool) *sourceEvaluation {
	data := llm.DecodeJSON(reply)
	if data == nil || data["text_score"] == nil || data["image_score"] == nil || (hasTaste && data["taste_score"] == nil) {
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
	PersonalBlog  string
	Score         int
	Images        []int
	Text          *int
	TextOnly      bool
	Reason        string
	Quality       jx.M
	Goodness      *GoodnessResult
	Taste         *int
	TasteIncluded bool
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
	judge := u.TopicFormatOf(topic).Judge
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
		return &sourceEvaluation{Score: s, Text: &s, TextOnly: s < config.DiscoveryTextRejectScore, Reason: text.Reason, PersonalBlog: text.PersonalBlog}
	}
	// Text-only feeds have no storyboard to score. An available text review
	// is sufficient for non-image topics; do not turn missing media into an
	// unavailable classifier result. Image-required topics remain strict.
	if text != nil && judge != "image" && !sourcePostsHaveImages(posts) {
		s := text.Score
		return &sourceEvaluation{Score: s, Text: &s, TextOnly: s < config.DiscoveryTextRejectScore, Reason: text.Reason}
	}
	review := u.sourceImagesReview(title, topic, posts)
	if review == nil {
		return nil
	}
	if text == nil {
		return &sourceEvaluation{Score: review.Score, Images: []int{review.Score}, Reason: review.Reason, PersonalBlog: review.PersonalBlog}
	}
	s := text.Score
	return &sourceEvaluation{Score: min(text.Score, review.Score), Images: []int{review.Score}, Text: &s, Reason: review.Reason, PersonalBlog: review.PersonalBlog}
}

func sourceEvaluationBasis(e *sourceEvaluation) string {
	if e.Text != nil {
		return fmt.Sprintf("подписи %d%%", *e.Text)
	}
	var s []string
	for _, x := range e.Images {
		s = append(s, fmt.Sprint(x))
	}
	return "картинки " + strings.Join(s, "/")
}

// ---- Доски Двача под тему ----

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

// ---- Проверка кандидатов ----

// storeDiscoveredSource — итог проверки → каталог, предложения или отказ: added | suggested | rejected | failed.
// Предложение нужно и по теме, и по годноте; очередь предложений темы ограничена (сверх — лучшие по баллу). Итог пишется в реестр.
func (u *U) storeDiscoveredSource(c DiscoveryCandidate, info jx.M, ev *sourceEvaluation, topic string, activity jx.M, extra int) string {
	score := 0
	var q jx.M
	if ev != nil {
		score, q = ev.Score, ev.Quality
	}
	sid := SourceIDFor(c.Platform, c.ID)
	result := "rejected"
	// Automatic admission needs both strong relevance and enough evidence.
	// Samples without a goodness assessment stay in the review queue.
	autoScore := max(u.TInt("auto_add_score"), 90)
	if discoveryAutoEligible(score, autoScore+extra, ev, len(jx.Maps(info, "posts"))) {
		result = "added"
	} else if score >= u.TInt("suggest_score")+extra {
		result = "suggested"
	}
	rejectReason := ""
	if ev != nil {
		rejectReason = ev.Reason
	}
	if result != "rejected" && ev != nil && ev.Goodness != nil && ev.Goodness.Score < u.TInt("goodness_min") {
		rejectReason = fmt.Sprintf("не годнота: %d%% (%s)", ev.Goodness.Score, ev.Goodness.Note)
		result = "rejected"
	}
	title := jx.Str(info, "title")
	tn := TopicNorm(topic)
	_, err := u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		switch result {
		case "added":
			e := jx.M{"id": info["id"], "name": title, "topics": []any{TopicBase(topic)}, "auto": true, "score": score, "preview": SourcePreviewImages(jx.Maps(info, "posts"))}
			if qq := TopicQueryOf(topic); qq != "" {
				e["query"] = qq
			}
			if activity != nil {
				e["activity"] = activity
			}
			if q != nil {
				e["audience"] = jx.M{"subscribers": q["subscribers"], "view_rate": q["view_rate"], "checked_at": now()}
			}
			l := d.List(CatalogKind(c.Platform))
			found := false
			for _, existing := range *l {
				if strings.EqualFold(jx.Str(existing, "id"), jx.Str(e, "id")) {
					u.addApprovedSource(d, jx.M{"platform": c.Platform, "id": e["id"], "name": e["name"]}, topic)
					found = true
					break
				}
			}
			if !found {
				*l = append(*l, e)
			}
		case "suggested":
			reason := fmt.Sprintf("Найден автоподбором по теме «%s» (%s): оценка %d%%", topic, c.Origin, score)
			if q != nil {
				reason += fmt.Sprintf(" (по теме %d%%), ", jx.Int(q, "content")) + DescribeSourceAudience(jx.Int(q, "subscribers"), floatPtr(q["view_rate"]), q)
			} else if s := jx.Int(info, "subscribers"); s > 0 {
				reason += ", подписчиков " + FormatSubscribers(s)
			}
			if activity != nil {
				reason += ", " + DescribeSourceActivity(activity)
			}
			if ev != nil && ev.Goodness != nil {
				reason += fmt.Sprintf(", годнота %d%% (%s)", ev.Goodness.Score, ev.Goodness.Note)
			}
			if ev != nil && strings.TrimSpace(ev.Reason) != "" {
				reason += ". " + trimRunes(strings.TrimSpace(ev.Reason), 180)
			}
			if desc := jx.Str(info, "description"); desc != "" {
				reason += ". " + trimRunes(desc, 160)
			} else {
				reason += "."
			}
			d.Suggestions = append(d.Suggestions, jx.M{"source_id": sid, "platform": c.Platform, "id": info["id"], "name": SourceDisplayName(c.Platform, title),
				"score": score, "topic": topic, "timestamp": now(), "origin": jx.M{"kind": c.OriginKind, "detail": c.Origin}, "refs": []any{}, "reason": reason})
		}
		return true
	})
	if err != nil {
		// каталог не сохранён — и реестр не пишем: кандидат найдётся снова
		u.Log("Каталог не сохранён, %s не записан: %v", sid, err)
		return "failed"
	}
	reg := registryWrite{SourceID: sid, Platform: c.Platform, Name: title, URL: registryURL(c.Platform, c.ID), Status: RegStatusRejected, Checked: true,
		Topic: tn, Result: &registryTopic{Score: score, Reason: rejectReason, At: now()}, Origin: jx.M{"kind": c.OriginKind, "detail": c.Origin},
		Sample: registrySample(jx.Maps(info, "posts"))}
	switch result {
	case "added":
		reg.Status = RegStatusCatalog
	case "suggested":
		reg.Status = RegStatusSuggested
	}
	if q != nil {
		reg.Metrics = jx.M{}
		for k, v := range q {
			reg.Metrics[k] = v
		}
		if activity != nil {
			reg.Metrics["activity"] = activity
		}
	}
	if err := u.registryUpsert(reg); err != nil {
		u.Log("Реестр источников: не сохранён %s: %v", sid, err)
	}
	return result
}

func floatPtr(v any) *float64 {
	if v == nil {
		return nil
	}
	f := jx.ToFloat(v)
	return &f
}

func (u *U) discoveryCheapNote(c DiscoveryCandidate, reason string) {
	if u.run == nil {
		return
	}
	g := DiscoveryGroupOf(c.Platform, c.ID)
	if u.run.DiscoveryCheap[g] == nil {
		u.run.DiscoveryCheap[g] = map[string]int{}
	}
	u.run.DiscoveryCheap[g][reason]++
}

func rejectInfo(c DiscoveryCandidate) jx.M {
	return jx.M{"id": c.ID, "title": c.ID, "description": "", "posts": []any{}}
}

// checkDiscoveryCandidate — проверка одного кандидата. Возвращает ссылки на другие площадки и признак «не загрузилось».
func (u *U) checkDiscoveryCandidate(c DiscoveryCandidate, topic string, stats map[string]int) (links []DiscoveryCandidate, failed bool) {
	label := DiscoveryLabel(c)
	phase := func(action string) {
		if u.discoveryPhase != nil {
			u.discoveryPhase(action + ": " + label)
		}
	}
	phase("Проверяем реестр")
	extra := 0
	if c.Depth >= 2 {
		extra = config.DiscoveryDeepExtraScore
	}
	sid := SourceIDFor(c.Platform, c.ID)
	verdict, why, row := u.RegistryVerdict(sid, topic)
	if verdict == regSkip {
		u.discoveryCheapNote(c, "реестр: "+why)
		return nil, false
	}
	if verdict == regSample {
		u.registrySampleCheck(c, topic, row, stats)
		return nil, false
	}
	stats["checked"]++
	reject := func(info jx.M) {
		stats["rejected"]++
		u.storeDiscoveredSource(c, info, nil, topic, nil, 0)
	}
	if u.UASourceBlocked(sid, c.Info) != "" {
		u.Log("-> %s: украинский источник — пропуск.", label)
		reject(rejectInfo(c))
		u.registryUpsert(registryWrite{SourceID: sid, Platform: c.Platform, Status: RegStatusUkraine, Reason: "украинский источник"})
		return nil, false
	}
	pre := ""
	if c.Platform == PlatformTelegram {
		pre = c.ID + " "
	}
	if DiscoveryLanguageReject(pre + jx.Str(c.Info, "title")) {
		u.discoveryCheapNote(c, "не на том языке")
		reject(rejectInfo(c))
		return nil, false
	}
	phase("Загружаем источник и посты")
	info, err := u.discoveryProbe(c, topic)
	if u.Ctx.Err() != nil {
		return nil, true
	}
	if err != nil {
		if errors.Is(err, ErrDiscoveryMissing) {
			u.Log("-> %s: %s", label, strings.TrimPrefix(err.Error(), ErrDiscoveryMissing.Error()+": "))
			reject(rejectInfo(c))
			u.registryUpsert(registryWrite{SourceID: sid, Platform: c.Platform, Status: RegStatusDead, Reason: "пустой, закрытый или удалённый источник"})
			return nil, false
		}
		stats["failed"]++
		u.discoveryCandidateError(c, err)
		u.registryUpsert(registryWrite{SourceID: sid, Platform: c.Platform, Status: RegStatusFailed, Reason: err.Error(), Checked: true, Failed: true,
			Topic: TopicNorm(topic), Result: &registryTopic{Reason: "не загрузилось", At: now()}})
		return nil, true
	}
	if alt := jx.Str(info, "alt_id"); alt != "" {
		u.discoveryMarkChecked(topic, PlatformRSS, alt)
	}
	posts := jx.Maps(info, "posts")
	linked := linkedCandidates(info)
	subs := jx.Int(info, "subscribers")
	views := viewPtrs(info["post_views"])
	if len(views) == 0 {
		for _, p := range posts {
			if jx.Has(p, "views") {
				v := jx.Int(p, "views")
				views = append(views, &v)
			}
		}
	}
	rate := SourceViewRate(subs, views)
	var times []int64
	for _, t := range jx.List(info, "post_times") {
		times = append(times, jx.ToInt(t))
	}
	if len(times) == 0 {
		for _, p := range posts {
			times = append(times, jx.Int(p, "posted_at"))
		}
	}
	phase("Проверяем активность и аудиторию")
	activity := SourceActivity(times, 0)
	title := label + " «" + jx.Str(info, "title") + "»"
	inactive := SourceInactivityReason(activity, 0)
	if inactive == "" {
		inactive = SourceDeadAudienceReason(subs, rate)
	}
	if inactive != "" {
		stats["rejected"]++
		u.discoveryCheapNote(c, "мёртвых")
		u.storeDiscoveredSource(c, info, nil, topic, nil, 0)
		return linked, false
	}
	var fresh []jx.M
	for _, p := range posts {
		if !IsPostOutdated(jx.Int(p, "posted_at"), 0, config.ContentMaxAgeDays) {
			fresh = append(fresh, p)
		}
	}
	posts = fresh
	named := c.OriginKind == "tg_name" || c.OriginKind == "model_handle"
	var hit *bool
	if !named {
		if c.Depth >= 1 {
			hit = u.discoveryTitleHit(topic, jx.Str(info, "title"), jx.Str(info, "description"))
		} else {
			var texts []string
			for _, p := range posts {
				texts = append(texts, jx.Str(p, "text"))
			}
			hit = u.discoveryKeywordHit(topic, jx.Str(info, "title"), jx.Str(info, "description"), texts)
		}
	}
	// Picture-led VK posts may have no caption at all. The image model can
	// judge these; absence of a keyword is not evidence of a mismatch.
	if c.Platform == PlatformVK && hit != nil && !*hit && u.TopicFormatOf(topic).Judge != "text" {
		meaningful := false
		for _, post := range posts {
			caption := strings.TrimSpace(jx.Str(post, "text"))
			if caption != "" && caption != EmptyPostText {
				meaningful = true
				break
			}
		}
		if !meaningful {
			hit = nil
		}
	}
	if hit != nil && !*hit {
		where := "в канале"
		if c.Depth >= 1 {
			where = "в названии и описании"
		}
		u.Log("-> %s: нет слов темы %s — пропуск без проверки картинок.", title, where)
		reject(info)
		return nil, false
	}
	if DiscoveryLanguageReject(jx.Str(info, "title") + " " + jx.Str(info, "description")) {
		u.Log("-> %s: язык названия и описания не тот — пропуск без проверки картинок.", title)
		reject(info)
		return nil, false
	}
	uaPosts := 0
	for _, p := range posts {
		if u.UAItemBlocked(p) != "" {
			uaPosts++
		}
	}
	if u.UASourceBlocked(sid, info) != "" || uaPosts >= 2 {
		u.Log("-> %s: украинский источник — пропуск без проверки картинок.", title)
		reject(info)
		u.registryUpsert(registryWrite{SourceID: sid, Platform: c.Platform, Status: RegStatusUkraine, Reason: "украинский источник"})
		return nil, false
	}
	phase("Оцениваем соответствие теме")
	var ev *sourceEvaluation
	if len(posts) > 0 {
		ev = u.evaluateSource(info, posts, topic)
	}
	if u.Ctx.Err() != nil {
		return nil, true
	}
	if ev == nil {
		u.Log("-> %s: нет постов с картинками — пропуск.", title)
		reject(info)
		return linked, false
	}
	q := SourceQualityScore(float64(ev.Score), subs, rate)
	q["subscribers"] = subs
	if rate != nil {
		q["view_rate"] = *rate
	} else {
		q["view_rate"] = nil
	}
	ev.Quality = q
	ev.Score = int(jx.Int(q, "score"))
	phase("Оцениваем качество источника")
	g := u.candidateGoodness(c, posts, rate, activity)
	ev.Goodness = &g
	if u.Ctx.Err() != nil {
		return nil, true
	}
	phase("Сохраняем результат")
	result := u.storeDiscoveredSource(c, info, ev, topic, activity, extra)
	stats[result]++
	if ev.TextOnly {
		u.Log("-> %s: по названию и постам не по теме (%d%%) — пропуск без проверки картинок.", title, *ev.Text)
		return nil, false
	}
	reason := ""
	if ev.Reason != "" {
		reason = ", " + ev.Reason
	}
	u.Log("-> %s: по теме %d%% (%s%s), %s, %s, глубина %d → %d%% — %s.", title, jx.Int(q, "content"), sourceEvaluationBasis(ev), reason,
		DescribeSourceAudience(subs, rate, q), DescribeSourceActivity(activity), c.Depth, ev.Score,
		map[string]string{"added": "добавлен в каталог", "suggested": "в предложения", "rejected": "не подходит", "failed": "не сохранено"}[result])
	return linked, false
}

func (u *U) discoveryCandidateError(c DiscoveryCandidate, err error) {
	message := fmt.Sprintf("%s: %s", DiscoveryLabel(c), err)
	if c.Platform == PlatformRSS {
		u.discoveryError(message)
	} else {
		u.Log("-> %s", message)
	}
}

func viewPtrs(v any) []*int64 {
	switch x := v.(type) {
	case []*int64:
		return x
	case []any:
		var out []*int64
		for _, e := range x {
			if e == nil {
				out = append(out, nil)
				continue
			}
			n := jx.ToInt(e)
			out = append(out, &n)
		}
		return out
	}
	return nil
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

// discoveryMarkChecked — альтернативный адрес того же источника отмечен проверенным по теме.
func (u *U) discoveryMarkChecked(topic, platform, id string) {
	u.registryUpsert(registryWrite{SourceID: SourceIDFor(platform, id), Platform: platform, Status: RegStatusRejected, Checked: true,
		Topic: TopicNorm(topic), Result: &registryTopic{Reason: "альтернативный адрес этого же источника", At: now()}})
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

// discoveryModelHandles — адреса каналов, которые знает умная модель (кэш по теме на 14 дней). Каждый потом проверяется.
func (u *U) discoveryModelHandles(topic string, plan discoveryPlan) []string {
	key := u.discoveryScopedKey("discovery_handles:" + u.discoveryScopedTopic(topic))
	var cached []string
	if u.CacheGet(u.Ctx, key, &cached) {
		return uniqueStrings(append(cached, plan.Telegram...))
	}
	d := u.Catalog()
	approved := u.discoveryApprovedNames(d, topic)
	rejected := u.discoveryRejectedNames(d.Votes, topic)
	prompt := u.prompt("telegram_discovery", map[string]string{"topic": u.TopicPromptContext(topic) + "." + u.TopicProfileHint(topic), "approved": string(jx.Marshal(approved)), "rejected": string(jx.Marshal(rejected))})
	schema := jx.M{"type": "object", "properties": jx.M{"handles": jx.M{"type": "array", "items": jx.M{"type": "string"}}}, "required": []string{"handles"}}
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Schema: schema, MaxTokens: 400, Temperature: 0.2, UserID: u.ID})
	if err != nil {
		return plan.Telegram
	}
	handles := discoveryCleanHandles(jx.Strs(llm.DecodeJSON(r.Text), "handles"))
	u.CacheSet(u.Ctx, key, handles, config.DiscoveryNamesMaxDays*24*time.Hour)
	if len(handles) > 0 {
		u.Log("-> Каналы, которые знает модель: @%s.", strings.Join(handles, ", @"))
	}
	return uniqueStrings(append(handles, plan.Telegram...))
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

func (u *U) discoveryTopicAbandoned(topic string) bool {
	return u.ID != "" && u.run != nil && !u.run.FixedTopic && !u.TopicSame(u.CurrentTopic(), topic)
}

// RunSourceDiscovery is retired: only the fenced public source-autopilot
// worker may discover, review and connect new sources. The old scanner
// must not mutate the shared catalog or create a second approval queue.
func (u *U) RunSourceDiscovery(topic string, maxSeconds int) {
	// Kept as a compatibility entry point for old parser scheduling calls.
}

// StartSourceDiscovery starts a manual run immediately, independently of the collector.
func (u *U) StartSourceDiscovery(topic, platform string) string {
	return "auto_only"
}

func (u *U) startSourceDiscovery(topic string, maxSeconds int, manual bool, platform string) (*discoveryRun, string) {
	// Legacy discovery used to write both automatic catalog admissions and
	// manually reviewed suggestions. Its background callers are retained for
	// compatibility, but the pipeline is never launched.
	return nil, "auto_only"
}
// retiredSourceDiscoveryImplementation is kept temporarily for historical
// diagnostics; it must not be called from any public route.
func (u *U) retiredSourceDiscoveryImplementation(topic string, maxSeconds int, manual bool, platform string) (*discoveryRun, string) {
	if job, leased := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); leased && !autopilotMayUseSharedDiscovery(job, u.ID) {
		return nil, "invalid_scope"
	}
	// When a public profile is managed by the new worker, do not let the
	// legacy account-bound discovery independently mutate the same topic.
	if _, leased := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob); !leased && u.TBool("autopilot_auto") {
		var managed, active bool
		err := u.DB.Pool.QueryRow(u.Ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_demands WHERE topic_key=$1 AND mode='auto'), EXISTS(SELECT 1 FROM source_autopilot_demands WHERE topic_key=$1 AND mode='auto' AND NOT paused AND posts_per_day>0)`, autopilotTopicKey(topic)).Scan(&managed, &active)
		if err != nil {
			return nil, "unavailable"
		}
		if managed {
			if manual {
				if !active {
					return nil, "paused"
				}
				// The persistent scheduler creates the job if it does not yet exist.
				if err := u.App.reconcileAutopilotMode(u.Ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
					return nil, "unavailable"
				}
				return nil, "queued_auto"
			}
			return nil, "auto_managed"
		}
	}
	key := TopicNorm(topic)
	u.parsers.mu.Lock()
	if u.parsers.discoveryRuns[key] != nil {
		u.parsers.mu.Unlock()
		return nil, "running"
	}
	if manual {
		delete(u.parsers.discoveryPaused, key)
	}
	if u.parsers.discoveryPaused[key] {
		u.parsers.mu.Unlock()
		return nil, "paused"
	}
	if maxSeconds <= 0 {
		maxSeconds = u.TInt("discovery_minutes") * 60
	}
	parent := u.App.Context()
	_, leased := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob)
	if leased {
		// Preserve both fencing and cancellation when a worker loses its lease.
		parent = u.Ctx
	}
	ctx, cancel := context.WithCancel(parent)
	run := &discoveryRun{cancel: cancel, done: make(chan struct{})}
	u.parsers.discoveryRuns[key] = run
	u.parsers.mu.Unlock()
	uid := u.ID
	u.App.Go("ручной подбор источников", func(context.Context) {
		defer cancel()
		defer func() {
			u.parsers.mu.Lock()
			delete(u.parsers.discoveryRuns, key)
			close(run.done)
			u.parsers.mu.Unlock()
		}()
		release, owned, err := u.App.trySourceTopicOwnership(ctx, topic)
		if err != nil || !owned {
			u.Log("Подбор этой темы уже выполняется или временно недоступен.")
			return
		}
		defer release()
		w := u.App.User(ctx, uid, u.App.ParserLogger(uid))
		w.run = newRun(u.App)
		w.run.FixedTopic = leased
		w.runSourceDiscovery(topic, maxSeconds, manual, platform)
	})
	return run, "started"
}

// StopSourceDiscovery pauses automatic discovery for this topic until manually started again.
func (u *U) StopSourceDiscovery(topic string) (bool, bool) {
	key := TopicNorm(topic)
	u.parsers.mu.Lock()
	u.parsers.discoveryPaused[key] = true
	run := u.parsers.discoveryRuns[key]
	if run != nil {
		run.cancel()
	}
	u.parsers.mu.Unlock()
	if run == nil {
		return false, false
	}
	select {
	case <-run.done:
		return true, false
	case <-time.After(5 * time.Second):
		return true, true
	}
}

func (u *U) runSourceDiscovery(topic string, maxSeconds int, manual bool, platform string) {
	u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool { d.Discovery["running_since"] = now(); return true })
	defer u.UpdateCatalog(autopilotCleanupContext(u.Ctx, u.App.Context()), func(d *SourceDB) bool { d.Discovery["running_since"] = 0; return true })
	u.runSourceDiscoveryLocked(topic, u.TInt("discovery_checks"), maxSeconds, manual, platform)
}

// DiscoveryDefer — отложенный кандидат (t.me не отдал страницу или пост прошёл отбор): проверится первым в следующем прогоне.
func (u *U) DiscoveryDefer(topic string, c DiscoveryCandidate, why string) {
	key := u.discoveryScopedTopic(topic)
	type deferred struct {
		Platform string `json:"platform"`
		ID       string `json:"id"`
		Origin   string `json:"origin"`
		Depth    int    `json:"depth"`
		At       int64  `json:"at"`
	}
	KVUpdate(u.App, u.Ctx, u.discoveryScopedKey("discovery_deferred"), 0, func(all map[string][]deferred, _ bool) *map[string][]deferred {
		if all == nil {
			all = map[string][]deferred{}
		}
		var list []deferred
		for _, d := range all[key] {
			if now()-d.At < config.DiscoveryDeferDays*config.DaySeconds && d.ID != c.ID {
				list = append(list, d)
			}
		}
		if len(list) < config.DiscoveryDeferMax {
			list = append(list, deferred{c.Platform, c.ID, c.Origin, c.Depth, now()})
		}
		all[key] = list
		return &all
	})
	u.Log("-> %s: %s — отложен до следующего прогона темы.", c.ID, why)
}

func (u *U) discoveryTakeDeferred(topic string) []DiscoveryCandidate {
	key := u.discoveryScopedTopic(topic)
	var taken []DiscoveryCandidate
	type deferred struct {
		Platform string `json:"platform"`
		ID       string `json:"id"`
		Origin   string `json:"origin"`
		Depth    int    `json:"depth"`
		At       int64  `json:"at"`
	}
	KVUpdate(u.App, u.Ctx, u.discoveryScopedKey("discovery_deferred"), 0, func(all map[string][]deferred, _ bool) *map[string][]deferred {
		for _, d := range all[key] {
			if now()-d.At < config.DiscoveryDeferDays*config.DaySeconds {
				taken = append(taken, DiscoveryCandidate{Platform: d.Platform, ID: d.ID, Origin: d.Origin, Depth: d.Depth, Deferred: true, OriginKind: "deferred"})
			}
		}
		delete(all, key)
		return &all
	})
	return taken
}

func (u *U) runSourceDiscoveryLocked(topic string, limit, maxSeconds int, manual bool, platform string) {
	started := time.Now()
	d := u.Catalog()
	u.Log("")
	u.Log("🔍 Поиск новых источников — тема «%s», до %d мин.", topic, maxSeconds/60)
	known := u.discoveryKnownIDs(d, topic)
	queue := newDiscoveryQueue()
	progress := KV(u.App, u.Ctx, u.discoveryProgressKey(topic), discoveryProgress{})
	progress.Errors = nil
	if manual && progress.Platform != platform {
		progress.Platform, progress.Active = platform, false
	}
	if !manual && !progress.Active {
		progress.Platform = ""
	}
	if !progress.Active {
		progress.Target, progress.Found, progress.Active = u.TInt("discovery_target"), 0, true
		progress.Checked, progress.Rejected, progress.Failed, progress.Added, progress.Suggested = 0, 0, 0, 0, 0
	}
	var parked []DiscoveryCandidate
	var retries []discoveryRetry
	for _, retry := range progress.Retries {
		c := retry.Candidate
		sid := SourceIDFor(c.Platform, c.ID)
		if retry.NextAt <= now() && sourceKnownWithLegacy(known, sid) {
			r := u.registryGet(sid)
			if r == nil || (r.Status != RegStatusFailed && !(r.Status == RegStatusDead && r.Reason == "не загрузился трижды")) {
				continue
			}
			retry.NextAt = max(now()+60, r.CheckedAt+discoveryRetryDelay(r.FailCount))
		}
		if retry.NextAt <= now() && discoveryMatchesPlatform(c, progress.Platform) {
			known[sid] = true
			c.Deferred = true
			queue.add(DiscoveryGroupOf(c.Platform, c.ID), c, true)
		}
		retries = append(retries, retry)
	}
	progress.Retries = retries
	for _, c := range progress.Pending {
		if !discoveryMatchesPlatform(c, progress.Platform) {
			parked = append(parked, c)
			continue
		}
		if !sourceKnownWithLegacy(known, SourceIDFor(c.Platform, c.ID)) {
			known[SourceIDFor(c.Platform, c.ID)] = true
			queue.add(DiscoveryGroupOf(c.Platform, c.ID), c, false)
		}
	}
	var current *DiscoveryCandidate
	stage, detail := "prepare", "Готовим запросы по теме"
	publishLive := func() {
		pending := 0
		for _, c := range parked {
			if discoveryMatchesPlatform(c, progress.Platform) {
				pending++
			}
		}
		for _, list := range queue.groups {
			pending += len(list)
		}
		u.discoveryLive.Store(u.discoveryProgressKey(topic), jx.M{"running": true, "active": progress.Active,
			"stage": stage, "detail": detail, "found": progress.Found, "target": progress.Target, "pending": pending,
			"checked": progress.Checked, "rejected": progress.Rejected, "failed": progress.Failed,
			"added": progress.Added, "suggested": progress.Suggested, "errors": append([]string{}, progress.Errors...), "retry_count": len(progress.Retries), "last_search": discoveryLastSearch(progress.Queries), "insight": discoveryQueryInsight(progress.Queries)})
	}
	setStage := func(next, text string) { stage, detail = next, text; publishLive() }
	publishLive()
	defer u.discoveryLive.Delete(u.discoveryProgressKey(topic))
	saveProgress := func() {
		publishLive()
		progress.Pending = append([]DiscoveryCandidate{}, parked...)
		if current != nil {
			progress.Pending = append(progress.Pending, *current)
		}
		for _, group := range discoveryGroupOrder {
			progress.Pending = append(progress.Pending, queue.groups[group]...)
		}
		ctx, cancel := context.WithTimeout(autopilotCleanupContext(u.Ctx, context.Background()), 5*time.Second)
		defer cancel()
		if err := u.KVSet(ctx, u.discoveryProgressKey(topic), progress, 0); err != nil {
			u.Log("Не удалось сохранить ход поиска источников: %v", err)
		}
	}
	defer saveProgress()
	previousIssue := u.discoveryIssue
	u.discoveryIssue = func(message string) {
		progress.Errors = append(progress.Errors, message)
		if len(progress.Errors) > 10 {
			progress.Errors = progress.Errors[len(progress.Errors)-10:]
		}
		saveProgress()
	}
	defer func() { u.discoveryIssue = previousIssue }()
	previousObservation := u.discoverySearchObservation
	u.discoverySearchObservation = func(platform, query, status string) {
		discoveryObserveQuery(&progress, platform, query, status)
		saveProgress()
	}
	defer func() { u.discoverySearchObservation = previousObservation }()
	saveProgress()
	report := map[string]map[string]int{}
	if u.run != nil {
		u.run.DiscoveryCheap = map[string]map[string]int{}
	}
	refs := map[string][]string{}
	bumpReport := func(group, field string, n int) {
		if report[group] == nil {
			report[group] = map[string]int{}
		}
		report[group][field] += n
	}
	newCandidates := 0
	add := func(c DiscoveryCandidate, front bool) {
		if !discoveryMatchesPlatform(c, progress.Platform) {
			return
		}
		if c.Platform == PlatformRSS {
			if cid, _, ok := YouTubeFeedChannel(c.ID); ok {
				c.ID = YouTubeChannelFeedURL(cid)
			}
		}
		sid := SourceIDFor(c.Platform, c.ID)
		group := DiscoveryGroupOf(c.Platform, c.ID)
		if sourceKnownWithLegacy(known, sid) {
			refs[sid] = append(refs[sid], c.Origin)
			bumpReport(group, "known", 1)
			return
		}
		known[sid] = true
		queue.add(group, c, front)
		discoveryRecordQuery(&progress, c, 1, 0, 0)
		newCandidates++
		if newCandidates%10 == 0 {
			saveProgress()
		}
		bumpReport(group, "found", 1)
	}
	u.DvachBoardsForTopic(topic, true)
	turn := int(jx.Int(jx.Map(d.Discovery, "turns"), strings.ToLower(strings.TrimSpace(topic))))
	plan := u.discoveryAIPlan(topic)
	plan.Queries = uniqueStrings(append(plan.Queries, progress.ExtraQueries...))
	if len(queue.kindCounts()) == 0 && progress.SearchRounds >= 2 && now()-progress.ExpandedAt >= 86400 && u.Alive() {
		setStage("prepare", "Ищем новые формулировки запросов")
		progress.ExpandedAt = now()
		saveProgress()
		extra := u.discoveryExpandQueries(topic, plan.Queries)
		progress.ExtraQueries = uniqueStrings(append(progress.ExtraQueries, extra...))
		if len(progress.ExtraQueries) > 40 {
			progress.ExtraQueries = progress.ExtraQueries[len(progress.ExtraQueries)-40:]
		}
		plan.Queries = uniqueStrings(append(plan.Queries, extra...))
		u.Log("-> Новых формулировок поиска: %d.", len(extra))
		saveProgress()
	}
	plan.Queries = discoveryRankQueries(plan.Queries, progress.Queries, turn)
	if len(plan.Queries) > 0 {
		u.Log("-> Запросы для поиска: %s.", strings.Join(plan.Queries, ", "))
	} else {
		u.Log("-> Запросы для поиска: нет.")
	}
	if u.discoveryTopicAbandoned(topic) {
		u.Log("-> Тема поиска сменилась — подбор по «%s» прерван.", topic)
		return
	}
	reasons := map[string]string{}
	if len(queue.kindCounts()) == 0 {
		progress.SearchRounds++
		setStage("search", "Восстанавливаем отложенных кандидатов")
		var deferred []DiscoveryCandidate
		if progress.Platform == "" {
			deferred = u.discoveryTakeDeferred(topic)
		}
		for i := len(deferred) - 1; i >= 0; i-- {
			add(deferred[i], true)
		}
		seeds := u.discoveryTopicSeeds(d, topic)
		// Preserve the best seeds and rotate the rest to retain exploration.
		if len(seeds) > 2 {
			seeds = append(seeds[:2:2], DiscoveryRotate(seeds[2:], turn*config.DiscoverySimilarSeeds)...)
		}
		var postAuthors []DiscoveryCandidate
		// Telegram: поиск через аккаунт, похожие на каналы темы, адреса от модели, авторы найденных постов
		if progress.Platform == "" || progress.Platform == "telegram" {
			setStage("search", "Ищем: Telegram и похожие каналы")
			for _, c := range u.discoveryFindTelegram(plan, seeds) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "telegram" {
			setStage("search", "Ищем адреса с помощью модели")
			if u.TBool("model_handles") {
				for _, h := range u.discoveryModelHandles(topic, plan) {
					add(DiscoveryCandidate{Platform: PlatformTelegram, ID: h, Origin: "адрес от модели", OriginKind: "model_handle"}, false)
				}
			}
		}
		if progress.Platform == "" || progress.Platform == "vk" {
			setStage("search", "Ищем: ВКонтакте")
			for _, c := range u.discoveryFindVK(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "youtube" {
			setStage("search", "Ищем: YouTube")
			for _, c := range u.discoveryFindYouTube(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "tiktok" {
			setStage("search", "Ищем: TikTok")
			for _, c := range u.discoveryFindTikTok(u.discoveryTikTokQueries(plan.Queries, topic)) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "ok" {
			setStage("search", "Ищем: Одноклассники")
			for _, c := range u.discoveryFindOK(plan.Queries, topic) {
				add(c, false)
			}
		}
		if progress.Platform == "" {
			setStage("search", "Ищем авторов постов по запросам")
			postAuthors = u.discoveryFindPosts(plan.Queries, topic)
			for _, c := range postAuthors {
				add(c, c.Front)
			}
		}
		if progress.Platform == "" || progress.Platform == "tumblr" {
			setStage("search", "Ищем: Tumblr")
			for _, c := range u.discoveryFindTumblr(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" {
			setStage("search", "Ищем: Lemmy")
			for _, c := range u.discoveryFindLemmy(plan.Queries, topic) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "reddit" {
			setStage("search", "Ищем: Reddit")
			for _, c := range u.discoveryFindReddit(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "bluesky" {
			setStage("search", "Ищем: Bluesky")
			for _, c := range u.discoveryFindBluesky(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "joyreactor" {
			setStage("search", "Ищем: JoyReactor")
			for _, c := range u.discoveryFindJoyAuthors(plan.Queries, topic) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "habr" {
			setStage("search", "Ищем: Хабр")
			for _, c := range u.discoveryFindHabr(plan.Queries, turn) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "rss" {
			setStage("search", "Ищем: новостные сайты и RSS")
			for _, c := range u.discoveryFindNewsFeeds(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "pinterest" {
			setStage("search", "Ищем: Pinterest")
			for _, c := range u.discoveryFindPinterest(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "pikabu" {
			setStage("search", "Ищем: Пикабу")
			for _, c := range u.discoveryFindPikabu(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "x" {
			setStage("search", "Ищем: X")
			for _, c := range u.discoveryFindX(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" {
			setStage("search", "Ищем: Discord")
			for _, c := range u.discoveryFindDiscord(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "threads" {
			setStage("search", "Ищем: Threads")
			for _, c := range u.discoveryFindThreads(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "instagram" {
			setStage("search", "Ищем: Meta")
			for _, c := range u.discoveryFindMeta(plan.Queries) {
				add(c, false)
			}
		}
		if progress.Platform == "" || progress.Platform == "dzen" {
			setStage("search", "Ищем: Дзен")
			for _, c := range u.discoveryFindDzen(plan.Queries, topic) {
				add(c, false)
			}
		}
		reasons["post_search"] = fmt.Sprintf("запросов %d, авторов %d", min(len(plan.Queries), u.TInt("post_search_queries")), len(postAuthors))
		if !u.VKIsConnected(u.Ctx) {
			if u.VKCanRead(u.Ctx) {
				reasons["vk"] = "поиск сообществ без входа в VK не работает; ссылки из постов читаются сервисным ключом"
			} else {
				reasons["vk"] = "поиск сообществ не работает: VK не подключён и нет сервисного ключа"
			}
		}
		if p := u.TikTokSearchPauseNote(u.Ctx); p != "" {
			reasons["tiktok"] = p
		}
		if progress.Platform == "" {
			setStage("search", "Ищем ссылки в известных источниках")
			for _, seed := range seeds[:min(len(seeds), config.DiscoverySnowballSeeds)] {
				if page := u.FetchTelegramChannelPage(seed, 0); page != "" {
					for _, h := range SourceLinkedSources(page) {
						h.Origin, h.OriginKind, h.Depth = "упомянут в @"+seed, "mentioned", 1
						add(h, false)
					}
				}
			}
		}
	}
	setStage("queue", "Сортируем кандидатов по теме и площадкам")
	queue.prioritize(u.DiscoveryTopicKeywords(topic), refs)
	saveProgress()
	counts := queue.counts()
	total := 0
	var parts []string
	for _, g := range discoveryGroupOrder {
		if n := counts[g]; n > 0 {
			total += n
			parts = append(parts, fmt.Sprintf("%s %d", discoveryGroupNames[g], n))
		}
	}
	u.Log("-> Кандидатов на проверку: %d (проверю до %d). По площадкам: %s.", total, limit, strings.Join(parts, ", "))
	stats := map[string]int{}
	similarCalls := 0
	snowball := func(c DiscoveryCandidate) {
		if c.Depth <= config.DiscoverySnowballMaxDepth {
			add(c, false)
		}
	}
	// бюджет проверок делится между видами находки по доле «да» владельца (минимум 10 % каждому виду)
	caps := DiscoveryBudget(limit, queue.kindCounts(), u.DiscoveryOriginReport(d, topic))
	u.Log("-> Бюджет проверок по видам находки (всего %d): %s.", limit, discoveryBudgetLines(caps, discoveryKindLabels))
	used := map[string]int{}
	drained := false
	for stats["checked"] < limit && progress.Found < progress.Target {
		c := queue.next()
		if c == nil {
			// очередь кончилась, а бюджет ещё не выбран: отложенные кандидаты проверяются по второму кругу
			ready, otherPlatforms := discoveryPartitionParked(parked, progress.Platform)
			for _, p := range ready {
				queue.add(DiscoveryGroupOf(p.Platform, p.ID), p, false)
			}
			parked, drained = otherPlatforms, true
			if len(ready) == 0 {
				break
			}
			continue
		}
		stop := ""
		switch {
		case !u.Alive():
			stop = ParserSessionEnded
		case u.discoveryTopicAbandoned(topic):
			stop = "Тема поиска сменилась — подбор по «" + topic + "» прерван."
		case time.Since(started) > time.Duration(maxSeconds)*time.Second:
			stop = "Достигнут лимит времени автоподбора."
		}
		if stop != "" {
			queue.add(DiscoveryGroupOf(c.Platform, c.ID), *c, true)
			u.Log("-> %s", stop)
			break
		}
		if !drained && caps[c.OriginKind] > 0 && used[c.OriginKind] >= caps[c.OriginKind] {
			parked = append(parked, *c)
			continue
		}
		current = c
		setStage("check", "Проверяем "+DiscoveryLabel(*c))
		saveProgress()
		used[c.OriginKind]++
		group := DiscoveryGroupOf(c.Platform, c.ID)
		one := map[string]int{}
		u.Log("-> Проверяю %s.", DiscoveryLabel(*c))
		candidateCtx, cancel := context.WithTimeout(u.Ctx, 8*time.Minute)
		worker := *u
		worker.Ctx = candidateCtx
		worker.discoveryPhase = func(text string) { setStage("check", text) }
		links, failed := worker.checkDiscoveryCandidate(*c, topic, one)
		timedOut := candidateCtx.Err() == context.DeadlineExceeded
		cancel()
		if timedOut {
			u.Log("-> %s: время проверки вышло; отложен для повторной попытки.", DiscoveryLabel(*c))
			one["failed"]++
		}
		for k, v := range one {
			stats[k] += v
		}
		bumpReport(group, "checked", one["checked"])
		bumpReport(group, "accepted", one["added"]+one["suggested"])
		bumpReport(group, "rejected", one["rejected"])
		bumpReport(group, "failed", one["failed"])
		discoveryRecordQuery(&progress, *c, 0, one["checked"], one["added"]+one["suggested"])
		if timedOut || failed || one["failed"] > 0 {
			progress.Retries = discoveryScheduleRetry(progress.Retries, *c, now())
			u.Log("-> %s: проверка отложена, повтор не раньше %s.", DiscoveryLabel(*c), time.Unix(progress.Retries[len(progress.Retries)-1].NextAt, 0).Format("02.01.2006 15:04"))
		} else {
			progress.Retries = discoveryRemoveRetry(progress.Retries, *c)
		}
		progress.Found += one["added"] + one["suggested"]
		progress.Checked += one["checked"]
		progress.Rejected += one["rejected"]
		progress.Failed += one["failed"]
		progress.Added += one["added"]
		progress.Suggested += one["suggested"]
		if u.Ctx.Err() != nil {
			// Keep the in-flight candidate; known sources are filtered on resume.
			saveProgress()
			break
		}
		current = nil
		if timedOut {
			saveProgress()
			continue
		}
		if failed || one["failed"] > 0 {
			u.Log("-> %s: повтор запланирован с задержкой.", DiscoveryLabel(*c))
			saveProgress()
			continue
		}
		if one["added"]+one["suggested"] == 0 {
			saveProgress()
			continue
		}
		if c.Platform == PlatformTelegram && similarCalls < 6 {
			similarCalls++
			sims := u.discoveryTelegramAccountChannels(nil, []string{c.ID})
			for i := len(sims) - 1; i >= 0; i-- {
				snowball(DiscoveryCandidate{Platform: PlatformTelegram, ID: sims[i].Username, Origin: "похож на @" + c.ID, OriginKind: "similar", Depth: c.Depth + 1})
			}
		}
		for _, l := range links {
			l.Origin, l.OriginKind, l.Depth = "упомянут в "+DiscoveryLabel(*c), "mentioned", c.Depth+1
			snowball(l)
		}
		saveProgress()
	}
	if progress.Found >= progress.Target {
		progress.Active = false
	}
	setStage("finish", "Сохраняем результаты")
	saveProgress()
	u.Log("-> Цель подбора: найдено %d из %d; в очереди %d.", progress.Found, progress.Target, len(progress.Pending))
	u.discoveryAddRefs(topic, refs)
	for _, line := range u.discoverySummaryLines(report, reasons) {
		u.Log("-> Сводка: %s", line)
	}
	u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		d.Discovery["topic"], d.Discovery["last_run"], d.Discovery["requested"] = topic, now(), false
		runs := jx.Map(d.Discovery, "runs")
		if runs == nil {
			runs = jx.M{}
		}
		runs[strings.ToLower(strings.TrimSpace(topic))] = now()
		d.Discovery["runs"] = runs
		turns := jx.Map(d.Discovery, "turns")
		if turns == nil {
			turns = jx.M{}
		}
		turns[strings.ToLower(strings.TrimSpace(topic))] = turn + 1
		d.Discovery["turns"] = turns
		return true
	})
	u.Log("✅ Поиск источников завершён за %.1f мин: в каталог %d, в предложения %d, проверено %d.", time.Since(started).Minutes(), stats["added"], stats["suggested"], stats["checked"])
	u.Log("")
}

var _ = url.QueryEscape
