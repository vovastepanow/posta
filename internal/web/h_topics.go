package web

import (
	"sort"

	"posta/internal/jx"
	"posta/internal/posta"
)

func init() {
	get("settings", settings)
	get("topic_record", topicRecord)
	get("topics", func(c *Ctx) (any, error) {
		var cur any
		if e := c.U.TopicLookup(c.U.CurrentTopic()); e != nil {
			cur = e.ID
		}
		return jx.M{"current": cur, "topics": topicsPublicList(c.U)}, nil
	})
	post("save_topic", saveTopic)
	post("save_second_topic", saveSecondTopic)
	post("save_query", saveQuery)
	post("save_filters", saveFilters)
	post("adult_allow", adultAllow)
	post("topic_reprofile", func(c *Ctx) (any, error) {
		e := c.U.TopicByID(c.Str("id"))
		if e == nil {
			return nil, fail(400, "Такой темы нет.")
		}
		cp := *e
		cp.Profile = nil
		_, err := c.A.TopicSave(c.R.Context(), &cp)
		return ok(nil), err
	})
	post("topic_parent", func(c *Ctx) (any, error) {
		e, err := c.U.TopicSetParent(c.Str("id"), c.Str("parent"))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"topic_info": c.U.TopicPublic(e, nil)}), nil
	})
	post("topic_rubric", func(c *Ctx) (any, error) {
		e, err := c.U.TopicSetRubric(c.Str("id"), c.Str("rubric"))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"topic_info": c.U.TopicPublic(e, nil)}), nil
	})
	get("topic_info", topicInfo)
	post("topic_format_save", topicFormatSave)
}

func settings(c *Ctx) (any, error) {
	u := c.U
	topic := u.CurrentTopic()
	var info any
	if e := c.A.TopicEnsure(c.R.Context(), topic); e != nil {
		info = u.TopicPublic(e, nil)
	}
	return jx.M{"topic": topic, "topic2": u.CurrentTopic2(), "query": u.CurrentQuery(), "topic_info": info, "filters": u.ContentFilterValues(),
		"adult_allowed": u.AdultAllowed(), "adult_available": c.A.TBool("adult_available"),
		"background_content_enabled": u.BackgroundContentEnabled(), "personal_blog_mode": u.PersonalBlogMode(topic)}, nil
}

func topicRecord(c *Ctx) (any, error) {
	e := c.U.TopicByID(c.Q("id"))
	if e == nil {
		return nil, fail(404, "Такой темы нет.")
	}
	return jx.M{"topic": e, "summary": c.U.TopicPublic(e, c.A.Catalog()), "health": c.U.TopicHealthPublic(e.Title)}, nil
}

func topicsPublicList(u *posta.U) []jx.M {
	d := u.Catalog()
	adult := u.AdultAllowed()
	var out []jx.M
	for _, e := range u.TopicRegistry() {
		if adult || !u.TopicIsRestricted(e.Title) {
			out = append(out, u.TopicPublic(e, d))
		}
	}
	key := func(t jx.M) (string, int, string) {
		parent := jx.Map(t, "parent")
		if parent != nil {
			return jx.Str(parent, "title"), 1, jx.Str(t, "title")
		}
		return jx.Str(t, "title"), 0, jx.Str(t, "title")
	}
	sort.SliceStable(out, func(i, j int) bool {
		a1, a2, a3 := key(out[i])
		b1, b2, b3 := key(out[j])
		if a1 != b1 {
			return a1 < b1
		}
		if a2 != b2 {
			return a2 < b2
		}
		return a3 < b3
	})
	if out == nil {
		out = []jx.M{}
	}
	return out
}

func saveTopic(c *Ctx) (any, error) {
	u := c.U
	typed := posta.NormalizeTopic(c.Str("topic"))
	if typed == "" {
		return nil, fail(400, "Тема поиска не может быть пустой.")
	}
	if d := u.TopicSaveDenied(typed); d != "" {
		return nil, fail(409, d)
	}
	previous, previousKey := u.CurrentTopic(), u.SearchTopicKey()
	stopped := !u.TopicSame(typed, previous) && c.A.StopParser(u.ID)
	res := u.TopicResolve(typed, jx.Bool(c.In, "force_new"))
	if d := u.TopicSaveDenied(res.Topic.Title); d != "" {
		if stopped {
			u.StartParser(true)
		}
		return nil, fail(409, d)
	}
	topic := res.Topic.Title
	if err := u.SaveSettings(jx.M{"topic": topic}); err != nil {
		return nil, err
	}
	lacks := u.TopicLacksSources(c.A.Catalog(), topic)
	parser := "unchanged"
	changed := !u.TopicSame(topic, previous)
	if changed {
		before := u.Pool()
		after, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
			out := []jx.M{}
			for _, i := range pool {
				if posta.TopicNorm(jx.Str(i, "topic")) != posta.TopicNorm(previousKey) {
					out = append(out, i)
				}
			}
			return out
		})
		if err != nil {
			return nil, err
		}
		c.A.MediaReleaseRemoved(c.R.Context(), before, after)
	}
	if (changed || stopped) && u.ContentPoolNeedsFill(u.SearchTopicKey()) {
		parser = u.StartParser(true)
	}
	return ok(jx.M{"topic": topic, "parser": parser, "typed": typed, "matched": res.Matched, "created": res.Created, "ai": res.AI, "discovery": lacks,
		"topic_info": u.TopicPublic(res.Topic, nil)}), nil
}

func saveSecondTopic(c *Ctx) (any, error) {
	u := c.U
	typed := posta.NormalizeTopic(c.Str("topic2"))
	if d := u.TopicSaveDenied(typed); d != "" {
		return nil, fail(409, d)
	}
	topic2 := ""
	if typed != "" {
		topic2 = u.TopicResolve(typed, false).Topic.Title
	}
	if d := u.TopicSaveDenied(topic2); d != "" {
		return nil, fail(409, d)
	}
	if topic2 != "" && u.TopicSame(topic2, u.CurrentTopic()) {
		return nil, fail(400, "Вторая тема совпадает с первой.")
	}
	if posta.TopicNorm(topic2) == posta.TopicNorm(u.CurrentTopic2()) {
		return ok(jx.M{"topic2": topic2, "parser": "unchanged"}), nil
	}
	if err := u.SaveSettings(jx.M{"topic2": topic2}); err != nil {
		return nil, err
	}
	if _, err := u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{} }); err != nil {
		return nil, err
	}
	return ok(jx.M{"topic2": topic2, "parser": u.StartParser(true)}), nil
}

func saveQuery(c *Ctx) (any, error) {
	u := c.U
	q := posta.NormalizeQuery(c.Str("query"))
	if d := u.TopicSaveDenied(q); d != "" {
		return nil, fail(409, d)
	}
	if posta.TopicNorm(q) == posta.TopicNorm(u.CurrentQuery()) {
		return ok(jx.M{"query": q, "parser": "unchanged"}), nil
	}
	if err := u.SaveSettings(jx.M{"query": q}); err != nil {
		return nil, err
	}
	// новый запрос: текущий проход по старому останавливается (до 5 с) и сразу стартует новый, без паузы между проходами
	return ok(jx.M{"query": q, "parser": u.RestartParser()}), nil
}

func saveFilters(c *Ctx) (any, error) {
	u := c.U
	before := u.ContentFilterValues()
	changes := jx.M{}
	for k := range before {
		if v, has := c.In[k]; has {
			changes[k] = jx.Truthy(v)
		}
	}
	if err := u.SaveSettings(changes); err != nil {
		return nil, err
	}
	after := u.ContentFilterValues()
	same := true
	for k := range after {
		if k != "auto_translate" && jx.Bool(after, k) != jx.Bool(before, k) {
			same = false
		}
	}
	if same {
		return ok(jx.M{"filters": after, "parser": "unchanged"}), nil
	}
	return ok(jx.M{"filters": after, "parser": u.StartParser(true)}), nil
}

func adultAllow(c *Ctx) (any, error) {
	u := c.U
	allowed := jx.Bool(c.In, "allowed")
	if allowed && !c.A.TBool("adult_available") {
		return nil, fail(403, "Контент 18+ на этом сайте выключен.")
	}
	if err := u.SetAdultAllowed(allowed); err != nil {
		return nil, err
	}
	reset := !allowed && u.TopicIsRestricted(u.CurrentTopic())
	if reset {
		c.A.StopParser(u.ID)
		u.SaveSettings(jx.M{"topic": c.A.TStr("default_topic")})
	}
	return ok(jx.M{"adult_allowed": allowed, "topic_reset": reset}), nil
}

func topicInfo(c *Ctx) (any, error) {
	u := c.U
	topic := u.CurrentTopic()
	e := u.TopicLookup(topic)
	children := []jx.M{}
	total := 0
	var parent any
	if e != nil {
		kids := u.TopicChildren(e)
		total = len(kids)
		for _, k := range kids[:min(12, len(kids))] {
			children = append(children, jx.M{"title": k.Title, "key": k.Key})
		}
		if p := u.TopicParent(e); p != nil {
			parent = jx.M{"title": p.Title}
		}
	}
	return jx.M{"topic": topic, "format": u.TopicFormatOf(topic), "overridden": u.TopicFormatOverridden(topic), "children": children, "children_total": total, "parent": parent}, nil
}

func topicFormatSave(c *Ctx) (any, error) {
	u := c.U
	err := u.TopicFormatSave(u.CurrentTopic(), jx.M{"media": c.In["media"], "text": c.In["text"], "judge": c.In["judge"]}, jx.Bool(c.In, "reset"))
	if err != nil {
		return nil, err
	}
	return ok(jx.M{"format": u.TopicFormatOf(u.CurrentTopic())}), nil
}
