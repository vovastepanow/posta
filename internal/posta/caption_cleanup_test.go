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
		{"only signature", "Котики дня", "Котики дня"},
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
