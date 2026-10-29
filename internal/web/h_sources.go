package web

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"posta/internal/jx"
	"posta/internal/posta"
)

// Вкладка «Источники», «Свой контент», «Лучшие посты», «Зеркало», ролики по кнопке.

func init() {
	post("suggest_sources", func(c *Ctx) (any, error) {
		u, cancel := c.userDetached(4 * time.Minute)
		defer cancel()
		r, status, err := u.SuggestSources(c.In)
		if err != nil {
			return nil, err
		}
		if status != 200 {
			return Resp{status, r}, nil
		}
		return r, nil
	})
	post("source_vote", func(c *Ctx) (any, error) {
		return ok(nil), c.U.SourceVote(c.Str("vote"), jx.Map(c.In, "source"), c.Str("reason"))
	})
	discover := func(c *Ctx) (any, error) {
		c.A.RequestSourceDiscovery()
		p := c.U.StartParser(true)
		msg := map[string]string{"started": "Автоподбор запущен. Ход поиска — в журнале.", "running": "Сборщик уже работает: автоподбор начнётся при следующем запуске.",
			"no_session": "Окно приложения неактивно: обновите страницу, и автоподбор начнётся."}[p]
		if msg == "" {
			msg = "Не удалось запустить сборщик на сервере."
		}
		if p == "unavailable" {
			return Resp{503, jx.M{"status": "error", "parser": p, "message": msg}}, nil
		}
		return jx.M{"status": "success", "parser": p, "message": msg}, nil
	}
	post("discover_sources", discover)
	post("control_sources", discover)
	get("discovery_status", discoveryStatus)
	post("source_mute", func(c *Ctx) (any, error) {
		sid := c.Str("source_id")
		if !regexp.MustCompile(`^[a-z]+:\S{1,600}$`).MatchString(sid) {
			return nil, fail(400, "Некорректный источник.")
		}
		on := jx.Bool(c.In, "on")
		return ok(jx.M{"source_id": sid, "on": on}), c.U.SourceOverrideSet(sid, on)
	})
	get("topic_sources", func(c *Ctx) (any, error) {
		topic := c.U.CurrentTopic()
		var info any
		if e := c.U.TopicLookup(topic); e != nil {
			info = c.U.TopicPublic(e, c.A.Catalog())
		}
		return jx.M{"topic": topic, "topic_info": info, "sources": c.U.TopicSources()}, nil
	})
	post("remove_topic_source", func(c *Ctx) (any, error) {
		return ok(nil), c.U.RemoveTopicSource(c.Str("platform"), c.Str("id"), c.Str("name"))
	})
	post("add_topic_source", func(c *Ctx) (any, error) {
		link := c.Str("link")
		if len([]rune(link)) > posta.SourceAddMaxLink {
			return nil, fail(400, "Ссылка слишком длинная.")
		}
		if err := topicAvailable(c); err != nil {
			return nil, err
		}
		u, cancel := c.userDetached(3 * time.Minute)
		defer cancel()
		r, err := u.SourceAddManual(link, u.CurrentTopic())
		if err != nil {
			return nil, err
		}
		return ok(r), nil
	})
	post("own_upload", ownUpload)
	post("own_import_csv", ownImportCSV)
	post("own_import_x", func(c *Ctx) (any, error) {
		var links []string
		if l := jx.Strs(c.In, "links"); len(l) > 0 {
			links = l
		} else {
			links = regexp.MustCompile(`\r?\n`).Split(c.Str("links"), -1)
		}
		u, cancel := c.userDetached(3 * time.Minute)
		defer cancel()
		r, err := u.XImportLinks(links)
		if err != nil {
			return nil, err
		}
		return ok(r), nil
	})
	post("own_import_yadisk", func(c *Ctx) (any, error) {
		u, cancel := c.userDetached(2 * time.Minute)
		defer cancel()
		r, err := u.OwnImportYadisk(c.Str("url"))
		if err != nil {
			return nil, err
		}
		return ok(r), nil
	})
	get("best_posts", func(c *Ctx) (any, error) {
		if err := topicAvailable(c); err != nil {
			return nil, err
		}
		link := strings.TrimSpace(c.Q("link"))
		if link == "" || len([]rune(link)) > posta.SourceAddMaxLink {
			return nil, fail(400, "Вставьте ссылку на канал, паблик или группу.")
		}
		typ := c.Q("type")
		if typ == "" {
			typ = "any"
		}
		if !contains([]string{"any", "photo", "album", "video"}, typ) {
			return nil, fail(400, "Неизвестный тип постов.")
		}
		days, _ := strconv.Atoi(c.Q("days"))
		if days == 0 {
			days = 7
		}
		by := c.Q("sort")
		if by == "" {
			by = "date"
		}
		phrase := strings.TrimSpace(c.Q("phrase"))
		if r := []rune(phrase); len(r) > 100 {
			phrase = string(r[:100])
		}
		u, cancel := c.userDetached(3 * time.Minute)
		defer cancel()
		r, err := u.BestPostsSearch(link, days, by, typ, phrase)
		if err != nil {
			return nil, err
		}
		return ok(posta.BestPostsPublic(r)), nil
	})
	post("best_posts_take", func(c *Ctx) (any, error) {
		if err := topicAvailable(c); err != nil {
			return nil, err
		}
		u, cancel := c.userDetached(5 * time.Minute)
		defer cancel()
		n, err := u.BestPostsTake(c.Str("key"), jx.Strs(c.In, "tokens"))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"added": n}), nil
	})
	get("mirror_list", func(c *Ctx) (any, error) { return ok(jx.M{"mirrors": c.U.MirrorList()}), nil })
	post("mirror_save", func(c *Ctx) (any, error) {
		if err := topicAvailable(c); err != nil {
			return nil, err
		}
		m, err := c.U.MirrorSave(c.In)
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"mirror": m}), nil
	})
	post("mirror_delete", func(c *Ctx) (any, error) {
		c.U.MirrorDelete(c.Str("id"))
		return ok(nil), nil
	})
	post("translate_text", func(c *Ctx) (any, error) {
		text := strings.TrimSpace(c.Str("text"))
		if text == "" || !jx.Bool(c.U.Settings(), "auto_translate") {
			return jx.M{}, nil
		}
		hit := c.U.TranslateToRussian(text, 6*time.Second)
		if hit == nil || !hit.OK || hit.Text == "" {
			return jx.M{}, nil
		}
		return jx.M{"text": hit.Text, "lang": hit.Lang, "note": "Переведено с " + posta.LanguageNameGenitive(hit.Lang)}, nil
	})
	post("video_fetch", videoFetch)
	get("video_status", videoStatus)
	post("telegram_video_fetch", func(c *Ctx) (any, error) {
		if posta.VideoPageVia(c.Str("url")) != "tgu" {
			return nil, fail(400, "Нужен адрес поста Telegram: https://t.me/канал/номер.")
		}
		return videoFetch(c)
	})
	get("telegram_video_status", func(c *Ctx) (any, error) {
		if posta.VideoPageVia(c.Q("url")) != "tgu" {
			return nil, fail(400, "Нужен адрес поста Telegram: https://t.me/канал/номер.")
		}
		return videoStatus(c)
	})
}

func topicAvailable(c *Ctx) error {
	if t := c.U.CurrentTopic(); c.U.TopicSaveDenied(t) != "" {
		return fail(400, "Тема «"+t+"» недоступна: выберите другую тему.")
	}
	return nil
}

func discoveryStatus(c *Ctx) (any, error) {
	u := c.U
	d := c.A.Catalog()
	topic := u.CurrentTopic()
	auto, frozen, sugg := 0, 0, 0
	for _, ck := range posta.CatalogKinds {
		for _, e := range *d.List(ck.Kind) {
			if !u.CatalogEntryMatchesTopic(e, topic, false) {
				continue
			}
			if jx.Bool(e, "auto") {
				auto++
			}
			if jx.Has(e, "frozen_until") {
				frozen++
			}
		}
	}
	for _, s := range d.Suggestions {
		if u.TopicsAreEquivalent(jx.Str(s, "topic"), topic) {
			sugg++
		}
	}
	var info any
	if e := c.A.TopicEnsure(c.R.Context(), topic); e != nil {
		info = u.TopicPublic(e, d)
	}
	disc := d.Discovery
	running := now()-jx.Int(disc, "running_since") < int64(c.A.TInt("discovery_minutes")*60+120)
	return jx.M{"enabled": c.A.TBool("discovery"), "topic": topic, "topic_info": info, "last_topic": jx.Str(disc, "topic"), "last_run": jx.Int(disc, "last_run"),
		"requested": jx.Bool(disc, "requested"), "running": running, "auto_added": auto, "frozen": frozen, "suggestions": sugg, "vk_connected": c.A.VKIsConnected(c.R.Context())}, nil
}

func now() int64 { return time.Now().Unix() }

// multipartFiles — файлы поля name во временные файлы (удаляет cleanup).
func multipartFiles(c *Ctx, field string, maxTotal int64) ([]posta.Upload, func(), error) {
	cleanup := func() {}
	if err := c.R.ParseMultipartForm(32 << 20); err != nil {
		if c.R.ContentLength > maxTotal {
			return nil, cleanup, fail(413, "Слишком много за раз: уберите часть файлов.")
		}
		return nil, cleanup, fail(400, "Не удалось прочитать загрузку.")
	}
	var out []posta.Upload
	var paths []string
	cleanup = func() {
		for _, p := range paths {
			os.Remove(p)
		}
		if c.R.MultipartForm != nil {
			c.R.MultipartForm.RemoveAll()
		}
	}
	for _, fh := range c.R.MultipartForm.File[field] {
		f, err := fh.Open()
		if err != nil {
			out = append(out, posta.Upload{Name: fh.Filename, Err: "Не удалось загрузить файл."})
			continue
		}
		tmp, err := os.CreateTemp("", "posta-upload-")
		if err != nil {
			f.Close()
			return nil, cleanup, err
		}
		io.Copy(tmp, f)
		f.Close()
		tmp.Close()
		paths = append(paths, tmp.Name())
		out = append(out, posta.Upload{Name: fh.Filename, Path: tmp.Name()})
	}
	return out, cleanup, nil
}

func ownUpload(c *Ctx) (any, error) {
	files, cleanup, err := multipartFiles(c, "files", 200<<20)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	u, cancel := c.userDetached(2 * time.Minute)
	defer cancel()
	r, err := u.OwnUpload(files, c.R.FormValue("text"), c.R.FormValue("album") == "1")
	if err != nil {
		return nil, err
	}
	return ok(r), nil
}

func ownImportCSV(c *Ctx) (any, error) {
	files, cleanup, err := multipartFiles(c, "csv", 4<<20)
	defer cleanup()
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fail(400, "Выберите CSV-файл.")
	}
	data, err := os.ReadFile(files[0].Path)
	if err != nil {
		return nil, fail(400, "Не удалось загрузить файл.")
	}
	u, cancel := c.userDetached(5 * time.Minute)
	defer cancel()
	r, err := u.OwnImportCSV(data)
	if err != nil {
		return nil, err
	}
	return ok(r), nil
}

const tguNoAccountText = "Для больших видео подключите личный аккаунт Telegram: Личный кабинет → Соцсети."

func videoFetch(c *Ctx) (any, error) {
	link := c.Str("url")
	via := posta.VideoPageVia(link)
	if via == "" {
		return nil, fail(400, "Нужен адрес поста Telegram, видео TikTok или ролика YouTube (watch или Shorts).")
	}
	if via == "tgu" && !c.U.TelegramUserConnected() {
		return nil, fail(409, tguNoAccountText)
	}
	state, limited := c.A.VideoFetchEnqueue(c.R.Context(), link, via, c.U.ID, true)
	if limited {
		return nil, fail(429, "Уже скачивается максимум роликов с этого сайта: дождитесь, пока они закончатся.")
	}
	return state, nil
}

func videoStatus(c *Ctx) (any, error) {
	link := c.Q("url")
	if posta.VideoPageVia(link) == "" {
		return nil, fail(400, "Нужен адрес поста Telegram, видео TikTok или ролика YouTube (watch или Shorts).")
	}
	st := c.A.VideoPreviewStatus(c.R.Context(), link)
	st["progress"] = c.A.VideoProgressPublic(c.R.Context(), c.U.ID, posta.VideoPreviewKey(link))
	return st, nil
}
