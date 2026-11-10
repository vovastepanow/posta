package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestCleanPostAttribution(t *testing.T) {
	src := jx.M{"source": "Telegram | Котики дня", "source_url": "https://t.me/cats_daily/123"}
	tests := []struct{ name, text, want string }{
		{"signature", "Кот спит.\n\nКотики дня", "Кот спит."},
		{"source label", "Кот спит.\nИсточник: Котики дня", "Кот спит."},
		{"channel story", "Наш канал перекрыли из-за ремонта.", "Наш канал перекрыли из-за ремонта."},
		{"handle", "Кот спит.\n@cats_daily", "Кот спит."},
		{"link", "Кот спит.\nhttps://t.me/cats_daily", "Кот спит."},
		{"promotion", "Кот спит.\nПодписывайтесь на наш канал!", "Кот спит."},
		{"sentences", "Кот спит. Подписывайтесь на канал! Завтра будет видео.", "Кот спит. Завтра будет видео."},
		{"source mentioned in story", "Котики дня опубликовали исследование.", "Котики дня опубликовали исследование."},
		{"only advertising", "Подписывайтесь на канал!", "Подписывайтесь на канал!"},
		{"only signature", "Котики дня", ""},
		{"source topics", "Мемы | Видео | Котики дня", ""},
		{"unrelated topics", "Мемы | Видео | Юмор", "Мемы | Видео | Юмор"},
		{"source post link retained", "Новость.\nhttps://t.me/cats_daily/123", "Новость.\nhttps://t.me/cats_daily/123"},
		{"ads as subject", "Реклама лекарств стала темой исследования.", "Реклама лекарств стала темой исследования."},
		{"advertising label", "Кот спит.\n#реклама", "Кот спит."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CleanPostAttribution(tt.text, src); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTelegramCaptionDoesNotUseChannelTitle(t *testing.T) {
	page := `<div class="tgme_channel_info_header_title">Мемы | Видео | Юмор</div>` +
		`<div data-post="humor_channel/123"><a class="tgme_widget_message_photo_wrap" style="background-image:url('https://example.com/photo.jpg')"></a>` +
		`<div class="tgme_widget_message_text">Мемы | Видео | Юмор</div></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "humor_channel", "name": "Telegram | Юмор"}, 1, false, 0)
	if len(posts) != 1 || jx.Str(posts[0], "text") != EmptyPostText {
		t.Fatalf("unexpected posts: %#v", posts)
	}
}

func TestCleanPostDisplayText(t *testing.T) {
	tests := []struct{ text, want string }{
		{`Текст [\#BookMemes](https://mastodon.art/tags/BookMemes) [#Meme](https://mastodon.art/tags/Meme)`, `Текст`},
		{"Текст #книги #Books\n\n#ещё", "Текст"},
		{`Читайте [статью](https://example.com/a)`, `Читайте статью`},
		{`Текст [@reading](https://example.com/@reading) [@humor@lemmy.world](https://lemmy.world/c/humor)`, `Текст`},
		{`Текст **@reading** **@humor@fedigroups.social** **@humor@lemmy.world** **@aiop**&#x20;`, `Текст`},
		{`Пишите name@example.com`, `Пишите name@example.com`},
		{`Адрес https://example.com/page#section`, `Адрес https://example.com/page#section`},
		{`#только_тег`, ``},
	}
	for _, tt := range tests {
		if got := CleanPostDisplayText(tt.text, jx.M{}); got != tt.want {
			t.Errorf("CleanPostDisplayText(%q) = %q, want %q", tt.text, got, tt.want)
		}
	}
}
