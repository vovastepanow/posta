package posta

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
	"posta/internal/llm"
)

// Стиль канала и подпись от ИИ: стиль описывается один раз по свежим постам канала и хранится в channels.style,
// подпись пишется одним коротким запросом с этим описанием.

const (
	channelStylePosts      = 20
	channelStyleMinPost    = 40
	channelStyleSample     = 700
	channelStyleMaxSummary = 1200
	channelStyleStaleDays  = 30
	captionMaxChars        = 1024
)

func (u *U) styleJob(channelID string) jx.M {
	return KV(u.App, u.Ctx, "channel_style_job:"+channelID, jx.M{})
}

// ChannelStylePublic — описание стиля для интерфейса.
func (u *U) ChannelStylePublic(c *Channel) jx.M {
	s := c.Style
	job := u.styleJob(c.ID)
	busy := jx.Str(job, "status") == "running" && now()-jx.Int(job, "at") < 600
	updated := jx.Int(s, "updated_at")
	errText := ""
	if !busy && jx.Str(job, "status") == "error" {
		errText = jx.Str(job, "message")
	}
	return jx.M{"summary": jx.Str(s, "summary"), "source": jx.Str(s, "source"), "updated_at": updated,
		"stale": updated > 0 && jx.Str(s, "source") == "auto" && now()-updated > channelStyleStaleDays*86400,
		"can_learn": c.ChatUsername != "", "busy": busy, "error": errText}
}

func (u *U) channelStyleSamples(c *Channel) []string {
	var texts []string
	add := func(t string) {
		t = strings.TrimSpace(t)
		if t != EmptyPostText && runeLen(t) >= channelStyleMinPost && !contains(texts, t) {
			texts = append(texts, t)
		}
	}
	if c.ChatUsername == "" {
		return nil
	}
	if page := u.TelegramChannelPageCached(c.ChatUsername, 0); page != "" {
		for _, p := range TelegramPostsFromHTML(page, jx.M{"id": c.ChatUsername}, channelStylePosts, true, 0) {
			add(jx.Str(p, "text"))
		}
	}
	for _, e := range u.Published(50) {
		if strings.Contains(jx.Str(e, "url"), "t.me/"+c.ChatUsername+"/") {
			add(jx.Str(e, "text"))
		}
	}
	if len(texts) > channelStylePosts {
		texts = texts[:channelStylePosts]
	}
	return texts
}

func channelStylePrompt(samples []string) string {
	var b strings.Builder
	for i, t := range samples {
		fmt.Fprintf(&b, "[%d] %s\n\n", i+1, trimRunes(t, channelStyleSample))
	}
	return "Ниже последние посты одного Telegram-канала. Опиши, КАК автор пишет, чтобы другой человек мог писать так же.\n\n" + b.String() +
		"Опиши по пунктам, коротко и конкретно (всего до 700 символов, каждый пункт одной-двумя фразами, без вступлений и без Markdown-разметки): язык и тон; обращение к читателю («ты»/«вы»/без); длина постов и абзацев; " +
		"эмодзи (есть ли, сколько, какие, где стоят); оформление (жирный, списки, заглавные, многоточия); хештеги и подписи в конце; шутки и приёмы; что автор никогда не делает. " +
		"Не пересказывай темы постов и не цитируй их целиком."
}

// channelStyleTrim — режем по концу абзаца или предложения, а не посреди слова.
func channelStyleTrim(text string) string {
	text = strings.TrimSpace(text)
	if runeLen(text) <= channelStyleMaxSummary {
		return text
	}
	cut := string([]rune(text)[:channelStyleMaxSummary])
	end := max(strings.LastIndex(cut, "\n"), strings.LastIndex(cut, ". "), strings.LastIndex(cut, ".)"))
	if end > len(cut)/2 {
		cut = cut[:end+1]
	}
	return strings.TrimRight(cut, " \n")
}

// ChannelStyleLearn — изучить стиль канала по постам и сохранить.
func (u *U) ChannelStyleLearn(channelID string) (jx.M, error) {
	c := u.Channel(channelID)
	if c == nil {
		return nil, Conflict("Такого канала нет.")
	}
	if c.ChatUsername == "" {
		return nil, Bad("У закрытого канала нет публичной страницы: опишите стиль вручную.")
	}
	samples := u.channelStyleSamples(c)
	if len(samples) < 3 {
		return nil, Bad("В канале мало постов с текстом (нашлось %d, нужно хотя бы 3): опишите стиль вручную.", len(samples))
	}
	resp, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleText, Prompt: channelStylePrompt(samples), MaxTokens: 500, Temperature: 0.3, UserID: u.ID})
	if err != nil {
		return nil, Bad("Модель не ответила: %s", err)
	}
	summary := channelStyleTrim(strings.ReplaceAll(llm.StripCJK(resp.Text), "**", ""))
	if runeLen(summary) < 40 {
		return nil, Bad("Модель не смогла описать стиль: попробуйте ещё раз.")
	}
	// Два коротких поста целиком — образец (модель точнее копирует манеру по живому тексту)
	examples := []string{}
	for _, t := range samples {
		if runeLen(t) <= 500 && len(examples) < 2 {
			examples = append(examples, trimRunes(t, 300))
		}
	}
	style := jx.M{"summary": summary, "examples": examples, "source": "auto", "updated_at": now()}
	return style, u.ChannelUpdateField(channelID, "style", style)
}

var manyBreaksRe = regexp.MustCompile(`(\r?\n){3,}`)

// ChannelStyleSaveManual — описание стиля, написанное владельцем; пустое — стиль стирается.
func (u *U) ChannelStyleSaveManual(channelID, summary string) (jx.M, error) {
	c := u.Channel(channelID)
	if c == nil {
		return nil, Conflict("Такого канала нет.")
	}
	summary = strings.TrimSpace(manyBreaksRe.ReplaceAllString(summary, "\n\n"))
	if runeLen(summary) > channelStyleMaxSummary {
		return nil, Bad("Описание стиля — не длиннее %d символов.", channelStyleMaxSummary)
	}
	style := jx.M{}
	if summary != "" {
		style = jx.M{"summary": summary, "examples": orAny(jx.List(c.Style, "examples")), "source": "manual", "updated_at": now()}
	}
	return style, u.ChannelUpdateField(channelID, "style", style)
}

func orAny(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

// CaptionQuotaTake — не больше captions_per_hour подписей от ИИ в час на пользователя.
func (u *U) CaptionQuotaTake() error {
	limit := u.TInt("captions_per_hour")
	key := "caption:" + u.ID + ":" + time.Now().Format("2006010215")
	n, _ := KVUpdate(u.App, u.Ctx, key, 2*time.Hour, func(n int, _ bool) *int { n++; return &n })
	if n > limit {
		return Bad("Слишком много подписей за час (до %d): попробуйте позже.", limit)
	}
	return nil
}

func captionPrompt(c *Channel, about, original string, limit int, current string) string {
	var b strings.Builder
	b.WriteString("Ты автор Telegram-канала")
	if c.ChatTitle != "" {
		b.WriteString(" «" + c.ChatTitle + "»")
	}
	b.WriteString(". Напиши подпись к посту с картинкой или видео.\n\n")
	if s := jx.Str(c.Style, "summary"); s != "" {
		b.WriteString("Так пишет автор канала (стиль строго соблюдай):\n" + s + "\n\n")
	} else {
		b.WriteString("Стиль канала неизвестен: пиши коротко (одно-два предложения, до 150 символов), живо и по-человечески, с лёгкой иронией, не больше одного эмодзи.\n\n")
	}
	for i, e := range jx.Strs(c.Style, "examples") {
		fmt.Fprintf(&b, "Пример поста канала %d:\n%s\n\n", i+1, e)
	}
	if about != "" {
		b.WriteString("Что на картинке или видео:\n" + about + "\n\n")
	}
	if original != "" {
		b.WriteString("Исходный текст поста у другого источника (перепиши своими словами, не копируй, чужие названия каналов и ссылки не переноси):\n" + trimRunes(original, 800) + "\n\n")
	}
	if current != "" {
		b.WriteString("Черновик подписи, который нужно улучшить, сохранив смысл:\n" + trimRunes(current, 800) + "\n\n")
	}
	fmt.Fprintf(&b, "Требования: пиши на языке канала, не длиннее %d символов; не выдумывай факты, имена и цифры, которых нет выше; без ссылок и без @упоминаний; "+
		"не используй штампы и пафос («пусть это принесёт улыбку», «идеальная картина», «это тот случай»); не описывай картинку дословно, а подай её с характером; "+
		"ответь только текстом подписи, без кавычек, без слов «Вот подпись» и пояснений.", limit)
	return b.String()
}

// ChannelCaptionGenerate — подпись от ИИ в стиле канала. context: description, original, current.
func (u *U) ChannelCaptionGenerate(channelID string, context jx.M) (string, error) {
	c := u.Channel(channelID)
	if c == nil {
		return "", Conflict("Такого канала нет.")
	}
	clip := func(k string) string { return strings.TrimSpace(trimRunes(jx.Str(context, k), 1500)) }
	about, original, current := clip("description"), clip("original"), clip("current")
	if about == "" && original == "" && current == "" {
		return "", Bad("Не о чем писать: у записи нет ни описания, ни текста. Напишите пару слов в поле текста, и ИИ их разовьёт.")
	}
	if err := u.CaptionQuotaTake(); err != nil {
		return "", err
	}
	resp, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleText, Prompt: captionPrompt(c, about, original, captionMaxChars-100, current), MaxTokens: 500, Temperature: 0.8, UserID: u.ID})
	if err != nil {
		return "", Bad("Модель не ответила: %s", err)
	}
	text := strings.Trim(strings.TrimSpace(llm.StripCJK(resp.Text)), " \t\n\r\"«»")
	if text == "" {
		return "", Bad("Модель не написала подпись: попробуйте ещё раз.")
	}
	return trimRunes(text, captionMaxChars), nil
}

// ChannelStyleLearnAsync — изучение стиля в фоне; ход виден в ChannelStylePublic (busy / error). true — запущено.
func (u *U) ChannelStyleLearnAsync(channelID string) (bool, error) {
	if u.Channel(channelID) == nil {
		return false, Conflict("Такого канала нет.")
	}
	started := false
	KVUpdate(u.App, u.Ctx, "channel_style_job:"+channelID, 0, func(job jx.M, _ bool) *jx.M {
		if jx.Str(job, "status") == "running" && now()-jx.Int(job, "at") < 600 {
			return nil
		}
		started = true
		return &jx.M{"status": "running", "at": now()}
	})
	if started {
		uid := u.ID
		u.Go("channel-style", func(ctx context.Context) {
			w := u.User(ctx, uid, nil)
			job := jx.M{"status": "done", "at": now()}
			if _, err := w.ChannelStyleLearn(channelID); err != nil {
				job = jx.M{"status": "error", "at": now(), "message": err.Error()}
			}
			w.KVSet(ctx, "channel_style_job:"+channelID, job, 24*time.Hour)
		})
	}
	return started, nil
}
