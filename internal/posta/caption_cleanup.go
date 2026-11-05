package posta

import (
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
	changed, substantive := false, false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		plain := captionLineTrim(trimmed)
		isSource := names[strings.ToLower(plain)] && plain != ""
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
	if !changed || !substantive {
		return text
	}
	return strings.TrimSpace(strings.Join(kept, "\n"))
}
