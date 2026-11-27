package posta

import (
	"net/url"
	"strings"

	xhtml "golang.org/x/net/html"

	"posta/internal/config"
	"posta/internal/jx"
)

func calendarIsArticle(record jx.M) bool {
	return len(jx.Map(record, "article")) > 0 || jx.Bool(record, "article_source")
}

func calendarStructuredArticleURL(record jx.M) string {
	for _, raw := range []string{jx.Str(record, "url"), jx.Str(record, "source_url")} {
		u, err := url.Parse(raw)
		if err == nil && u.Scheme == "https" && u.User == nil && u.Port() == "" &&
			osnovaHosts[osnovaWWWRe.ReplaceAllString(strings.ToLower(u.Hostname()), "")] != "" && osnovaIDRe.MatchString(u.Path) {
			return u.String()
		}
	}
	return ""
}

func calendarSafeLink(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") || len(raw) > 2048 {
		return ""
	}
	return strings.NewReplacer("(", "%28", ")", "%29").Replace(u.String())
}

func calendarLink(label, raw string, styled bool) string {
	href := calendarSafeLink(raw)
	if href == "" {
		return label
	}
	label = strings.Join(strings.Fields(label), " ")
	if label == "" || label == href {
		return href
	}
	if styled {
		label = strings.NewReplacer("[", "［", "]", "］").Replace(label)
		return "[" + label + "](" + href + ")"
	}
	return label + " (" + href + ")"
}

func calendarMark(value, marker string, styled bool) string {
	if !styled || marker == "" {
		return value
	}
	lines := strings.Split(value, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.Contains(trimmed, marker) {
			continue
		}
		at := strings.Index(line, trimmed)
		lines[i] = line[:at] + marker + trimmed + marker + line[at+len(trimmed):]
	}
	return strings.Join(lines, "\n")
}

// The same limited markup syntax as the composer. Links take precedence
// over nested formatting, and platforms without markup receive visible URLs.
func calendarHTMLText(raw string, styled bool) string {
	doc, err := xhtml.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	markers := map[string]string{"b": "**", "strong": "**", "i": "__", "em": "__", "u": "++", "s": "~~", "del": "~~", "code": "`", "pre": "`"}
	var render func(*xhtml.Node, string, bool) string
	render = func(n *xhtml.Node, mark string, links bool) string {
		if n.Type == xhtml.TextNode {
			return calendarMark(n.Data, mark, styled && links)
		}
		if n.Type == xhtml.ElementNode {
			switch n.Data {
			case "script", "style", "template", "iframe", "object", "head":
				return ""
			case "br":
				return "\n"
			case "a":
				if links {
					var label strings.Builder
					for c := n.FirstChild; c != nil; c = c.NextSibling {
						label.WriteString(render(c, "", false))
					}
					for _, attr := range n.Attr {
						if attr.Key == "href" {
							return calendarLink(label.String(), attr.Val, styled)
						}
					}
					return label.String()
				}
			}
			if m := markers[n.Data]; m != "" {
				mark = m
			}
		}
		var out strings.Builder
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			out.WriteString(render(c, mark, links))
		}
		switch n.Data {
		case "p", "div", "li", "blockquote":
			out.WriteByte('\n')
		}
		return out.String()
	}
	return strings.TrimSpace(render(doc, "", true))
}

func calendarArticleLinks(text string, refs []any, styled bool) string {
	var out strings.Builder
	for _, ref := range refs {
		pair, ok := ref.([]any)
		if !ok || len(pair) != 2 {
			continue
		}
		label, href := jx.ToStr(pair[0]), jx.ToStr(pair[1])
		at := strings.Index(text, label)
		if label == "" || at < 0 || calendarSafeLink(href) == "" {
			continue
		}
		out.WriteString(text[:at])
		out.WriteString(calendarLink(label, href, styled))
		text = text[at+len(label):]
	}
	out.WriteString(text)
	return out.String()
}

func calendarStructuredArticle(article jx.M, fallbackTitle string, media []MediaItem, styled bool) (string, []MediaItem, error) {
	var parts []string
	body := false
	title := strings.TrimSpace(jx.Str(article, "title"))
	if title == "" {
		title = strings.TrimSpace(fallbackTitle)
	}
	if title != "" {
		parts = append(parts, calendarMark(title, "**", styled))
	}
	addImage := func(raw, name string) bool {
		src := calendarSafeLink(raw)
		if src == "" {
			return false
		}
		for _, m := range media {
			if m.Src == src {
				return true
			}
		}
		media = append(media, MediaItem{Kind: "image", Src: src, Name: name})
		return true
	}
	if cover := jx.Map(article, "cover"); len(cover) > 0 && !addImage(jx.Str(cover, "src"), "Обложка статьи") {
		return "", nil, Bad("Обложка статьи недоступна.")
	}
	for _, block := range jx.Maps(article, "blocks") {
		text := strings.TrimSpace(jx.Str(block, "text"))
		switch jx.Str(block, "type") {
		case "h", "p", "quote":
			if text != "" {
				text = calendarArticleLinks(text, jx.List(block, "links"), styled)
				if jx.Str(block, "type") == "h" && !strings.Contains(text, "](") {
					text = calendarMark(text, "**", styled)
				}
				parts = append(parts, text)
				body = true
			}
		case "list":
			var items []string
			for _, row := range jx.Maps(block, "items") {
				if text := strings.TrimSpace(jx.Str(row, "text")); text != "" {
					items = append(items, "• "+calendarArticleLinks(text, jx.List(row, "links"), styled))
				}
			}
			if len(items) > 0 {
				parts = append(parts, strings.Join(items, "\n"))
				body = true
			}
		case "image":
			if !addImage(jx.Str(block, "src"), "Изображение статьи") {
				return "", nil, Bad("Одно из изображений статьи недоступно.")
			}
			if caption := strings.TrimSpace(jx.Str(block, "caption")); caption != "" {
				parts = append(parts, caption)
			}
		default:
			return "", nil, Bad("Статья содержит видео или нестандартное вложение. Откройте её в редакторе.")
		}
	}
	if !body {
		return "", nil, Bad("Полный текст статьи недоступен.")
	}
	if len(media) > config.TelegramMaxMedia {
		return "", nil, Bad("В статье слишком много вложений. Откройте её в редакторе.")
	}
	return strings.Join(parts, "\n\n"), media, nil
}

func (u *U) calendarPrepareOriginal(record, item jx.M, channel *Channel) (string, []MediaItem, jx.M, error) {
	media, err := u.calendarRecordMedia(record)
	if err != nil {
		return "", nil, nil, err
	}
	styled := contains(ChannelExtraKinds(channel), "styled_text")
	extras := jx.M{}
	text := PostCaptionClean(jx.Str(item, "text"))
	if calendarIsArticle(record) {
		link := calendarStructuredArticleURL(record)
		if link == "" {
			return "", nil, nil, Bad("Статья требует проверки в редакторе: структура оригинала недоступна.")
		}
		article := u.OsnovaArticle(link, false)
		if article == nil {
			return "", nil, nil, Bad("Не удалось получить полную статью.")
		}
		text, media, err = calendarStructuredArticle(article, jx.Str(jx.Map(record, "article"), "title"), media, styled)
		if err != nil {
			return "", nil, nil, err
		}
	} else if raw := strings.TrimSpace(jx.Str(record, "text_html")); raw != "" {
		text = calendarHTMLText(raw, styled)
		if text == "" {
			return "", nil, nil, Bad("Не удалось сохранить форматированный оригинал. Откройте пост в редакторе.")
		}
	} else if !jx.Has(item, "text") {
		text = PostCaptionClean(jx.Str(record, "text"))
	}
	if styled && (calendarIsArticle(record) || jx.Str(record, "text_html") != "") {
		extras["styled_text"] = true
	}
	var kinds []string
	for _, m := range media {
		kinds = append(kinds, m.Kind)
	}
	extras, err = PublishExtrasForChannel(channel, text, kinds, nil, extras)
	return text, media, extras, err
}
