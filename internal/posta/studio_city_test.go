package posta

import (
	"testing"
	"time"
)

// Фиксированные даты: граница суток в поясе сервера, пустые дни, текущий день.
func TestStudioCityBuckets(t *testing.T) {
	loc := time.FixedZone("MSK", 3*3600)
	now := time.Date(2026, 10, 30, 15, 0, 0, 0, loc)
	start := StudioStart(now)
	if start.Weekday() != time.Sunday {
		t.Fatalf("старт — не воскресенье: %v", start)
	}
	days := studioDays(start, now)
	if want := 52*7 + int(now.Weekday()) + 1; days != want {
		t.Fatalf("дней %d, ждали %d", days, want)
	}
	_, off := now.Zone()
	at := func(y int, m time.Month, d, h, mi, s int) int64 { return time.Date(y, m, d, h, mi, s, 0, loc).Unix() }
	row := func(unix int64, topic string) studioDayRow {
		return studioDayRow{Day: studioDayOf(unix, off), Topic: topic, N: 1}
	}

	rows := []studioDayRow{
		row(at(2026, 10, 30, 0, 0, 0), "мемы"),     // полночь сегодня — последний день
		row(at(2026, 10, 29, 23, 59, 59), "мемы"),  // конец вчера — предпоследний
		row(at(2026, 10, 29, 0, 0, 0), "животные"), // другая тема — не считается
		row(start.Unix(), "мемы"),                  // первый день — ноль
		row(start.Unix()-1, "мемы"),                // до старта — отбрасывается
		row(at(2026, 10, 30, 23, 59, 59), "мемы"),  // сегодня, но после сейчас: всё ещё в окне
		row(at(2026, 10, 31, 0, 0, 0), "мемы"),     // завтра — вне окна
	}
	keep := func(topic string) bool { return topic == "мемы" }
	got := studioCounts(start, days, off, rows, keep)
	if len(got) != days {
		t.Fatalf("длина %d, ждали %d", len(got), days)
	}
	if got[0] != 1 || got[days-2] != 1 || got[days-1] != 2 {
		t.Fatalf("края: first=%d prev=%d today=%d", got[0], got[days-2], got[days-1])
	}
	sum := 0
	for _, v := range got {
		sum += v
	}
	if sum != 4 {
		t.Fatalf("сумма %d, ждали 4 (без животных, без старта−1 и без завтра)", sum)
	}
	// пустые дни — нули, никакого сдвига
	if got[days/2] != 0 {
		t.Fatalf("середина должна быть пустой: %d", got[days/2])
	}
	// без фильтра темы и для публикаций (keep=nil) считаются все строки
	if all := studioCounts(start, days, off, rows, nil); all[days-2] != 2 {
		t.Fatalf("без фильтра предпоследний день: %d, ждали 2 (мемы + животные за 29-е)", all[days-2])
	}
}
