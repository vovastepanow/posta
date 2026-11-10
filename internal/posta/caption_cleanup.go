package posta

import (
	"html"
	"net/url"
	"regexp"
	"strings"
	"unicode"

	"posta/internal/jx"
)

// Убираем только самостоятельные рекламные приписки: обычные упоминания брендов и ссылки на первоисточник остаются.
var captionPromoStart = regexp.MustCompile(`(?i)^(?:подписывай(?:тесь|ся)|подпиш(?:итесь|ись)|subscribe\b|follow\s+us\b|по\s+вопросам\s+рекламы|реклам[аы]\s*[:—–-]|на\s+правах\s+рекламы|#реклама|erid\s*[:=]|промокод\s*[:—–-]|заказать\s+рекламу|advertisement\s*[:—–-])`)
var captionSentenceEnd = regexp.MustCompile(`[.!?…]+(?:\s+|$)`)
var captionBareReference = regexp.MustCompile(`(?i)^(?:https?://\S+|@\w+|#\S+)$`)
var captionTokens = regexp.MustCompile(`!?\[[^\]\n]*\]\(https?://[^\s)]+\)|https?://[^\s<>]+|[\p{L}\p{N}._%+\-]+@[\p{L}\p{N}.\-]+\.[\p{L}]{2,}|\*\*@[\p{L}\p{N}_]+(?:@[\p{L}\p{N}_\-.]+)?\*\*|@[\p{L}\p{N}_]+(?:@[\p{L}\p{N}_\-.]+)?|\\?#[\p{L}\p{N}_]+`)
var captionSpaces = regexp.MustCompile(`[ \t]{2,}`)
var captionBlankLines = regexp.MustCompile(`\n[ \t]*\n(?:[ \t]*\n)+`)

// CleanPostDisplayText removes source hashtags and unwraps Markdown links, while
// preserving ordinary URLs (including fragments) and the labels of other links.
func CleanPostDisplayText(text string, item jx.M) string {
	text = html.UnescapeString(CleanPostAttribution(text, item))
	text = captionTokens.ReplaceAllStringFunc(text, func(token string) string {
		if strings.HasPrefix(token, "http://") || strings.HasPrefix(token, "https://") {
			return token
		}
		if !strings.HasPrefix(token, "@") && !strings.HasPrefix(token, "**@") && strings.Contains(token, "@") && !strings.HasPrefix(token, "[") {
			return token // email address
		}
		if strings.HasPrefix(token, "[") || strings.HasPrefix(token, "![") {
			start := strings.Index(token, "[") + 1
			end := strings.Index(token, "](")
			label := strings.TrimSpace(token[start:end])
			if strings.HasPrefix(strings.TrimPrefix(label, `\`), "#") || strings.HasPrefix(label, "@") {
				return ""
			}
			return label
		}
		return ""
	})
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(captionSpaces.ReplaceAllString(line, " "))
	}
	return strings.TrimSpace(captionBlankLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}

func captionLineTrim(s string) string {
	return strings.TrimSpace(strings.TrimFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) }))
}

func captionSourceNames(item jx.M) map[string]bool {
	names := map[string]bool{}
	source := strings.TrimSpace(jx.Str(item, "source"))
	if parts := strings.SplitN(source, " | ", 2); len(parts) == 2 {
		source = strings.TrimSpace(parts[1])
	}
	if n := strings.ToLower(captionLineTrim(source)); len([]rune(n)) >= 3 {
		names[n] = true
	}
	if n := strings.ToLower(captionLineTrim(jx.Str(item, "channel_title"))); n != "" {
		names[n] = true
	}
	for _, raw := range []string{jx.Str(item, "source_url"), jx.Str(item, "url")} {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), "t.me") {
			continue
		}
		name := strings.Split(strings.Trim(u.Path, "/"), "/")[0]
		if len(name) >= 3 {
			names[strings.ToLower(name)] = true
		}
	}
	return names
}

// CleanPostAttribution возвращает исходный текст, если нельзя отделить основной пост от рекламы.
func CleanPostAttribution(text string, item jx.M) string {
	names := captionSourceNames(item)
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	changed, substantive, removedSource := false, false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		plain := captionLineTrim(trimmed)
		isSource := names[strings.ToLower(plain)] && plain != ""
		// Название канала иногда сохранено в источнике сокращённо: «Юмор»
		// вместо «Мемы | Видео | Юмор». Сопоставляем только короткий список.
		segments := strings.Split(plain, "|")
		if !isSource && len(segments) >= 3 && len(segments) <= 5 {
			valid, matches := true, false
			for _, segment := range segments {
				part := captionLineTrim(segment)
				if part == "" || len([]rune(part)) > 30 || len(strings.Fields(part)) > 3 {
					valid = false
				}
				matches = matches || names[strings.ToLower(part)]
			}
			isSource = valid && matches
		}
		lower := strings.ToLower(trimmed)
		for _, prefix := range []string{"источник:", "source:", "канал:"} {
			if strings.HasPrefix(lower, prefix) && names[strings.ToLower(captionLineTrim(trimmed[len(prefix):]))] {
				isSource = true
			}
		}
		if strings.HasPrefix(trimmed, "@") {
			isSource = names[strings.ToLower(strings.TrimPrefix(trimmed, "@"))]
		}
		if u, err := url.Parse(trimmed); err == nil && strings.EqualFold(u.Hostname(), "t.me") {
			path := strings.Trim(u.Path, "/")
			isSource = !strings.Contains(path, "/") && names[strings.ToLower(path)]
		}
		if isSource {
			changed = true
			removedSource = true
			continue
		}
		// Удаляем отдельные рекламные предложения, не затрагивая соседние фразы.
		var parts []string
		from := 0
		for _, end := range captionSentenceEnd.FindAllStringIndex(line, -1) {
			parts = append(parts, line[from:end[1]])
			from = end[1]
		}
		if from < len(line) {
			parts = append(parts, line[from:])
		}
		var retained strings.Builder
		for _, part := range parts {
			if captionPromoStart.MatchString(strings.TrimSpace(part)) || captionPromoStart.MatchString(captionLineTrim(part)) {
				changed = true
				continue
			}
			retained.WriteString(part)
		}
		line = retained.String()
		trimmed = strings.TrimSpace(line)
		if trimmed == "" && plain != "" {
			continue
		}

		kept = append(kept, line)
		if trimmed != "" && !captionBareReference.MatchString(trimmed) {
			for _, r := range trimmed {
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					substantive = true
					break
				}
			}
		}
	}
	if !changed || (!substantive && !removedSource) {
		return text
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
