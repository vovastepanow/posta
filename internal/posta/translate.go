package posta

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
)

// Автоперевод текста постов на русский: модель роли «Перевод», если она включена в админке, иначе MyMemory.
// Кэш общий (таблица cache), квота MyMemory учитывается до конца суток UTC.

const (
	translateMaxChars  = 2000
	translateChunk     = 450
	translateTimeout   = 4 * time.Second
	translateRetry     = time.Hour
	translateCacheTTL  = 30 * 24 * time.Hour
)

var (
	ukLettersRe = regexp.MustCompile(`[іїєґІЇЄҐ]`)
	beLetterRe  = regexp.MustCompile(`[ўЎ]`)
)

// TextLanguageGuess — ru (переводить нечего), uk/be, ? (определит сервис), "" — букв мало.
func TextLanguageGuess(text string) string {
	clean := uaMentionRe.ReplaceAllString(uaLinkRe.ReplaceAllString(text, " "), " ")
	letters, cyr := 0, 0
	for _, r := range clean {
		if unicode.IsLetter(r) {
			letters++
			if unicode.Is(unicode.Cyrillic, r) {
				cyr++
			}
		}
	}
	if letters < 12 {
		return ""
	}
	if float64(cyr)/float64(letters) < 0.6 {
		return "?"
	}
	if ukLettersRe.MatchString(clean) {
		return "uk"
	}
	if beLetterRe.MatchString(clean) {
		return "be"
	}
	return "ru"
}

var langGenitive = map[string]string{"en": "английского", "de": "немецкого", "uk": "украинского", "be": "белорусского", "es": "испанского", "fr": "французского",
	"it": "итальянского", "pt": "португальского", "pl": "польского", "tr": "турецкого", "ja": "японского", "zh": "китайского", "ko": "корейского", "ar": "арабского",
	"nl": "нидерландского", "sv": "шведского", "cs": "чешского", "sr": "сербского", "bg": "болгарского", "ka": "грузинского", "kk": "казахского", "fi": "финского",
	"ro": "румынского", "hu": "венгерского", "el": "греческого", "he": "иврита", "hi": "хинди", "th": "тайского", "vi": "вьетнамского", "id": "индонезийского"}

func LanguageNameGenitive(code string) string {
	if n, ok := langGenitive[strings.ToLower(code)]; ok {
		return n
	}
	return "другого языка"
}

var protectRe = regexp.MustCompile(`https?://\S+|@\w+|#\w+`)

func translateProtect(text string) (string, map[string]string) {
	m := map[string]string{}
	n := 0
	out := protectRe.ReplaceAllStringFunc(text, func(s string) string {
		tok := fmt.Sprintf("ZXQ%dZXQ", n)
		n++
		m[tok] = s
		return tok
	})
	return out, m
}

var (
	paraRe     = regexp.MustCompile(`\n\s*\n`)
	sentenceRe = regexp.MustCompile(`([.!?…])\s+`)
)

type chunk struct{ text, join string }

// translateChunks — куски не длиннее maxLen: по абзацам, длинный абзац — по предложениям, длинное предложение — по словам.
func translateChunks(text string, maxLen int) []chunk {
	var out []chunk
	for _, p := range paraRe.Split(strings.TrimSpace(text), -1) {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		join := ""
		if len(out) > 0 {
			join = "\n\n"
		}
		buf, has := "", false
		for _, s := range strings.Split(sentenceRe.ReplaceAllString(p, "$1\x00"), "\x00") {
			if s = strings.TrimSpace(s); s == "" {
				continue
			}
			for _, piece := range splitWords(s, maxLen) {
				if has && runeLen(buf+" "+piece) <= maxLen {
					buf += " " + piece
					continue
				}
				if has {
					out = append(out, chunk{buf, join})
					join = " "
				}
				buf, has = piece, true
			}
		}
		if has {
			out = append(out, chunk{buf, join})
		}
	}
	return out
}

func splitWords(s string, maxLen int) []string {
	if runeLen(s) <= maxLen {
		return []string{s}
	}
	var out []string
	buf := ""
	for _, w := range strings.Fields(s) {
		if buf != "" && runeLen(buf+" "+w) > maxLen {
			out = append(out, buf)
			buf = ""
		}
		if buf == "" {
			buf = w
		} else {
			buf += " " + w
		}
	}
	if buf != "" {
		out = append(out, buf)
	}
	return out
}

func translateNormalize(s string) string { return strings.ToLower(collapseSpaces(s)) }

type translateHit struct {
	OK      bool   `json:"ok"`
	Text    string `json:"text"`
	Lang    string `json:"lang"`
	RetryAt int64  `json:"retry_at"`
}

// TranslateToRussian — перевод на русский или nil (русский, квота, сбой, бюджет, перевод совпал с исходником).
func (u *U) TranslateToRussian(text string, budget time.Duration) *translateHit {
	text = trimRunes(strings.TrimSpace(text), translateMaxChars)
	guess := TextLanguageGuess(text)
	if text == "" || guess == "" || guess == "ru" || (guess == "uk" && u.UAFilterOn()) {
		return nil
	}
	sum := md5.Sum([]byte(translateNormalize(text)))
	key := "translate:" + hex.EncodeToString(sum[:])
	var hit translateHit
	if u.CacheGet(u.Ctx, key, &hit) {
		if hit.OK {
			return &hit
		}
		if hit.RetryAt > now() {
			return nil
		}
	}
	remember := func(h translateHit) { u.CacheSet(u.Ctx, key, h, translateCacheTTL) }
	never := translateHit{RetryAt: 1 << 62}
	// Модель для перевода включена — переводит она (бесплатная модель OpenRouter или локальная)
	if u.LLM.Enabled(u.Ctx, llm.RoleTranslate) {
		r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleTranslate, Quiet: true, UserID: u.ID, MaxTokens: 1200, Temperature: 0.2, Timeout: budget + 20*time.Second,
			Prompt: "Переведи текст поста на русский язык. Сохрани ссылки, @упоминания, #хэштеги и переносы строк. Мемный и разговорный тон сохрани. Если текст уже на русском — верни его без изменений. Ответь только переводом, без пояснений.\n\nТекст:\n" + text})
		if err == nil {
			out := strings.TrimSpace(r.Text)
			if out == "" || translateNormalize(out) == translateNormalize(text) || TextLanguageGuess(out) != "ru" {
				remember(never)
				return nil
			}
			lang := guess
			if lang == "?" {
				lang = "en"
			}
			h := translateHit{OK: true, Text: out, Lang: lang}
			remember(h)
			return &h
		}
	}
	if until := KV(u.App, u.Ctx, "translate_quota_until", int64(0)); until > now() {
		return nil
	}
	protected, tokens := translateProtect(text)
	started := time.Now()
	lang := ""
	if guess != "?" {
		lang = guess
	}
	var parts []string
	for _, c := range translateChunks(protected, translateChunk) {
		if time.Since(started) > budget {
			return nil
		}
		q := url.Values{"q": {c.text}, "langpair": {"autodetect|ru"}}
		if config.TranslateEmail != "" {
			q.Set("de", config.TranslateEmail)
		}
		body, status, _ := u.HTTPGetStatus("https://api.mymemory.translated.net/get?"+q.Encode(), translateTimeout, nil)
		data := jx.Unmarshal(body)
		if st := jx.Int(data, "responseStatus"); st != 0 {
			status = int(st)
		}
		if jx.Bool(data, "quotaFinished") || status == 403 || status == 429 {
			until := time.Now().UTC().Truncate(24 * time.Hour).Add(24 * time.Hour).Unix()
			u.KVSet(u.Ctx, "translate_quota_until", until, 25*time.Hour)
			u.Log("-> Перевод: квота сервиса исчерпана до %s UTC", time.Unix(until, 0).UTC().Format("02.01 15:04"))
			return nil
		}
		rd := jx.Map(data, "responseData")
		tr := jx.Str(rd, "translatedText")
		if status != 200 || strings.TrimSpace(tr) == "" {
			remember(translateHit{RetryAt: time.Now().Add(translateRetry).Unix()})
			return nil
		}
		detected := strings.ToLower(jx.Str(rd, "detectedLanguage"))
		if lang == "" && detected != "" {
			lang = detected
		}
		if detected == "ru" {
			remember(never)
			return nil
		}
		parts = append(parts, c.join+tr)
	}
	out := strings.Join(parts, "")
	for tok, orig := range tokens {
		if !strings.Contains(out, tok) {
			remember(never)
			return nil
		}
		out = strings.ReplaceAll(out, tok, orig)
	}
	if translateNormalize(out) == translateNormalize(text) {
		remember(never)
		return nil
	}
	if lang == "" {
		lang = guess
	}
	h := translateHit{OK: true, Text: out, Lang: lang}
	remember(h)
	return &h
}

// TranslateDisplay — текст карточки переводится, если включено; оригинал — в text_original.
func (u *U) TranslateDisplay(resp jx.M) jx.M {
	if !jx.Bool(u.Settings(), "auto_translate") {
		return resp
	}
	text := strings.TrimSpace(jx.Str(resp, "text"))
	if text == "" || text == EmptyPostText {
		return resp
	}
	t := u.TranslateToRussian(text, 6*time.Second)
	if t == nil {
		return resp
	}
	resp["text_original"] = resp["text"]
	resp["text"] = t.Text
	if caps := jx.List(resp, "captions"); len(caps) > 0 {
		c := append([]any{}, caps...)
		c[0] = t.Text
		resp["captions"] = c
	}
	resp["translated_from"] = t.Lang
	resp["translated_note"] = "Переведено с " + LanguageNameGenitive(t.Lang)
	return resp
}

// TranslateWarmCache — перевод заранее, сразу после попадания записи в пул.
func (u *U) TranslateWarmCache(item jx.M) {
	if jx.Bool(u.Settings(), "auto_translate") && strings.TrimSpace(jx.Str(item, "text")) != "" {
		u.TranslateToRussian(jx.Str(item, "text"), 4*time.Second)
	}
}
