package posta

import (
	"context"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"posta/internal/jx"
	"posta/internal/llm"
)

// Хэштеги поста: рубрика (тема записи) → имя (entity) → формат. Словарь строится из TopicTree.

const HashtagMaxLen = 16
const hashtagCandidatesMax = 500

type HashtagEntry struct {
	Tag    string   `json:"tag"`
	Topics []string `json:"topics"` // ключи тем; ["*"] — любая (форматы)
	Kind   string   `json:"kind"`   // rubric | entity | format
}

type hashtagDict struct {
	tags    map[string]*HashtagEntry
	order   []string
	aliases map[string]string // синоним → канонический
	byTopic map[string][]string
}

var (
	dictOnce sync.Once
	dict     *hashtagDict
)

var hashtagSpaceRe = regexp.MustCompile(`[\s\-]+`)

// HashtagNorm — без «#», строчные, ё → е, пробелы и дефисы → «_».
func HashtagNorm(tag string) string {
	t := strings.ToLower(strings.TrimSpace(tag))
	t = strings.ReplaceAll(strings.ReplaceAll(t, "ё", "е"), "#", "")
	return strings.Trim(hashtagSpaceRe.ReplaceAllString(t, "_"), "_")
}

func hashtagDictionary() *hashtagDict {
	dictOnce.Do(func() {
		d := &hashtagDict{tags: map[string]*HashtagEntry{}, aliases: map[string]string{}, byTopic: map[string][]string{}}
		add := func(tag, kind string, topics []string, syns []string) {
			canon := HashtagNorm(tag)
			if canon == "" {
				return
			}
			if e, ok := d.tags[canon]; ok {
				e.Topics = uniqueStrings(append(e.Topics, topics...))
			} else {
				d.tags[canon] = &HashtagEntry{Tag: canon, Topics: topics, Kind: kind}
				d.order = append(d.order, canon)
			}
			for _, s := range syns {
				if n := HashtagNorm(s); n != "" && n != canon {
					if _, taken := d.tags[n]; !taken {
						d.aliases[n] = canon
					}
				}
			}
		}
		for _, b := range TopicTree {
			add(b.Rubric, "rubric", []string{b.Key}, nil)
			for _, c := range b.Children {
				add(c.Rubric, "rubric", []string{c.Key}, c.Synonyms)
			}
		}
		for _, b := range TopicTree {
			for _, c := range b.Children {
				for _, ent := range c.Entities {
					tag := strings.ReplaceAll(HashtagNorm(ent), "_", "")
					if HashtagValid(tag) {
						if _, ok := d.tags[tag]; !ok {
							add(tag, "entity", []string{c.Key}, nil)
						}
					}
				}
			}
		}
		for _, f := range FormatHashtags {
			add(f.Tag, "format", []string{"*"}, f.Synonyms)
		}
		for _, tag := range d.order {
			for _, t := range d.tags[tag].Topics {
				if t != "*" {
					d.byTopic[t] = append(d.byTopic[t], tag)
				}
			}
		}
		dict = d
	})
	return dict
}

// HashtagLookup — запись словаря по тексту (с «#», синонимом или каноническим) или nil.
func HashtagLookup(text string) *HashtagEntry {
	n := HashtagNorm(text)
	d := hashtagDictionary()
	if e, ok := d.tags[n]; ok {
		return e
	}
	if c, ok := d.aliases[n]; ok {
		return d.tags[c]
	}
	return nil
}

func HashtagsForTopic(key string) []string { return hashtagDictionary().byTopic[key] }

var hashtagValidRe = regexp.MustCompile(`^[\p{L}\p{N}_]{2,16}$`)
var digitsRe = regexp.MustCompile(`^\d+$`)

// HashtagValid — одно слово из букв и цифр (допустим «_»), 2–16 символов, не одни цифры.
func HashtagValid(tag string) bool { return hashtagValidRe.MatchString(tag) && !digitsRe.MatchString(tag) }

// HashtagRubric — рубрика темы: своя рубрика пользователя, иначе из словаря. Ветка — своя рубрика ветки.
func (u *U) HashtagRubric(key string) string {
	if key == "" {
		return ""
	}
	if u != nil && u.ID != "" {
		if own := jx.Str(jx.Map(u.settingsRaw(u.Ctx, u.ID), "hashtag_rubrics"), key); own != "" {
			return own
		}
	}
	for _, t := range HashtagsForTopic(key) {
		if hashtagDictionary().tags[t].Kind == "rubric" {
			return t
		}
	}
	return ""
}

// ContentEntityClean — поле entity от модели: без «#» и лишнего, до 40 символов.
func ContentEntityClean(v any) string {
	s := strings.ReplaceAll(llm.StripCJK(jx.ToStr(v)), "#", "")
	return trimRunes(strings.Trim(strings.TrimSpace(s), " \t\n\r«»\"'.,"), 40)
}

func ContentEntityRule() string {
	return "entity: одно главное имя собственное из записи — человек, игра, бренд, фильм, мем-персонаж — как его пишут в хэштеге," +
		" по-русски, если так принято (дота, скибиди, маск, wildberries). Нет явного имени — пустая строка. Не тема и не описание.\n"
}

// HashtagEntity — имя записи → тег: словарь (вид entity), иначе годное новое имя (копится в кандидаты).
func (a *App) HashtagEntity(entity string, note bool) string {
	n := HashtagNorm(entity)
	if n == "" {
		return ""
	}
	if f := HashtagLookup(n); f != nil {
		if f.Kind == "entity" {
			return f.Tag
		}
		return ""
	}
	tag := strings.ReplaceAll(n, "_", "")
	if !HashtagValid(tag) {
		return ""
	}
	if note {
		a.hashtagCandidateNote(tag, entity)
	}
	return tag
}

func (a *App) hashtagCandidateNote(tag, sample string) {
	type cand struct {
		Count    int    `json:"count"`
		LastSeen int64  `json:"last_seen"`
		Sample   string `json:"sample"`
	}
	KVUpdate(a, a.ctxOrBg(), "hashtag_candidates", 0, func(all map[string]cand, _ bool) *map[string]cand {
		if all == nil {
			all = map[string]cand{}
		}
		c := all[tag]
		all[tag] = cand{c.Count + 1, now(), trimRunes(sample, 60)}
		if len(all) > hashtagCandidatesMax {
			keys := make([]string, 0, len(all))
			for k := range all {
				keys = append(keys, k)
			}
			sort.Slice(keys, func(i, j int) bool {
				if all[keys[i]].Count != all[keys[j]].Count {
					return all[keys[i]].Count > all[keys[j]].Count
				}
				return all[keys[i]].LastSeen > all[keys[j]].LastSeen
			})
			for _, k := range keys[hashtagCandidatesMax:] {
				delete(all, k)
			}
		}
		return &all
	})
}

// HashtagFormat — формат записи по её виду: видео, статья, опрос, подборка, переписка.
func HashtagFormat(r jx.M) string {
	video := PostItemHasVideo(r) || (jx.Str(r, "video_via") != "" && !jx.Bool(r, "video_unavailable"))
	switch {
	case video:
		return "видео"
	case jx.Bool(r, "article"):
		return "статья"
	case jx.Bool(r, "poll"):
		return "опрос"
	case len(ContentGallery(r)) >= 3 || len(jx.List(r, "images")) >= 3:
		return "подборка"
	case jx.Str(r, "format") == "text" && jx.Str(r, "image") != "":
		return "переписка"
	}
	return ""
}

// PostHashtags — хэштеги поста: рубрика (из первой метки, иначе из темы) → имя → формат, с «#».
func (u *U) PostHashtags(r jx.M) []string {
	key := ""
	if tags := u.ContentTagsClean(r["tags"]); len(tags) > 0 {
		key = tags[0]
	} else if t := u.TopicLookup(jx.Str(r, "topic")); t != nil {
		key = t.Key
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range []string{u.HashtagRubric(key), u.HashtagEntity(jx.Str(r, "entity"), false), HashtagFormat(r)} {
		if t != "" && !seen[t] {
			seen[t] = true
			out = append(out, "#"+t)
		}
	}
	return out
}

var sourceHashtagRe = regexp.MustCompile(`(?:^|[^\p{L}\p{N}_])#([\p{L}\p{N}_]{2,40})`)

// PostSourceHashtags — хэштеги из текста поста-источника → канонические (до 8).
func PostSourceHashtags(item jx.M) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, m := range sourceHashtagRe.FindAllStringSubmatch(jx.Str(item, "text"), -1) {
		if f := HashtagLookup(m[1]); f != nil && !seen[f.Tag] {
			seen[f.Tag] = true
			out = append(out, f.Tag)
			if len(out) >= 8 {
				break
			}
		}
	}
	return out
}

// HashtagSuggest — подсказки тегов по началу слова.
func HashtagSuggest(q string, limit int) []jx.M {
	q = HashtagNorm(q)
	if q == "" {
		return []jx.M{}
	}
	d := hashtagDictionary()
	var out []jx.M
	seen := map[string]bool{}
	for _, tag := range d.order {
		if len(out) >= limit {
			break
		}
		if strings.HasPrefix(tag, q) {
			seen[tag] = true
			out = append(out, jx.M{"tag": tag, "kind": d.tags[tag].Kind})
		}
	}
	aliases := make([]string, 0, len(d.aliases))
	for a := range d.aliases {
		aliases = append(aliases, a)
	}
	sort.Strings(aliases)
	for _, al := range aliases {
		if len(out) >= limit {
			break
		}
		if tag := d.aliases[al]; strings.HasPrefix(al, q) && !seen[tag] {
			seen[tag] = true
			out = append(out, jx.M{"tag": tag, "kind": d.tags[tag].Kind})
		}
	}
	if out == nil {
		out = []jx.M{}
	}
	return out
}

// PublishHashtagsInput — хэштеги из редактора: до трёх годных тегов с «#».
func PublishHashtagsInput(raw any) []string {
	var list []any
	if s, ok := raw.(string); ok {
		json.Unmarshal([]byte(s), &list)
	} else {
		list = jx.ToList(raw)
	}
	out := []string{}
	seen := map[string]bool{}
	for _, t := range list {
		tag := strings.ReplaceAll(HashtagNorm(jx.ToStr(t)), "_", "")
		if HashtagValid(tag) && !seen[tag] {
			seen[tag] = true
			out = append(out, "#"+tag)
			if len(out) >= 3 {
				break
			}
		}
	}
	return out
}

// ChannelHashtags — теги, которые разрешает канал: off — никаких, rubric — только рубрики, full — все.
func (u *U) ChannelHashtags(c *Channel, tags []string) []string {
	if c == nil || c.Hashtags == "off" || c.Hashtags == "" {
		return []string{}
	}
	var own []string
	for _, v := range jx.Map(u.settingsRaw(u.Ctx, u.ID), "hashtag_rubrics") {
		own = append(own, jx.ToStr(v))
	}
	kind := func(t string) string {
		if contains(own, strings.TrimPrefix(t, "#")) {
			return "rubric"
		}
		if f := HashtagLookup(t); f != nil {
			return f.Kind
		}
		return ""
	}
	out := []string{}
	for _, t := range tags {
		k := kind(t)
		if k == "" {
			u.hashtagCandidateNote(strings.TrimPrefix(t, "#"), strings.TrimPrefix(t, "#"))
		}
		if c.Hashtags == "full" || k == "rubric" {
			out = append(out, t)
		}
	}
	return out
}

// TextWithHashtags — хэштеги последней строкой без повторов; не влезает в limit — теги снимаются с конца.
func TextWithHashtags(text string, tags []string, limit int) string {
	text = strings.TrimRight(text, " \t\n")
	var keep []string
	for _, t := range uniqueStrings(tags) {
		re := regexp.MustCompile(`(?:^|[^\p{L}\p{N}_])` + regexp.QuoteMeta(t) + `(?:$|[^\p{L}\p{N}_])`)
		if !re.MatchString(text) {
			keep = append(keep, t)
		}
	}
	for len(keep) > 0 {
		out := strings.Join(keep, " ")
		if text != "" {
			out = text + "\n\n" + out
		}
		if runeLen(out) <= limit {
			return out
		}
		keep = keep[:len(keep)-1]
	}
	return text
}

// TopicHashtagsPublic — хэштеги темы для интерфейса: рубрика, рубрика по умолчанию, имена, синонимы.
func (u *U) TopicHashtagsPublic(e *Topic) jx.M {
	d := hashtagDictionary()
	own := HashtagsForTopic(e.Key)
	var rubricDefault any
	ents := []string{}
	for _, t := range own {
		switch d.tags[t].Kind {
		case "rubric":
			if rubricDefault == nil {
				rubricDefault = t
			}
		case "entity":
			ents = append(ents, t)
		}
	}
	syns := []string{}
	for al, tag := range d.aliases {
		if contains(own, tag) {
			syns = append(syns, al)
		}
	}
	sort.Strings(syns)
	var rubric any
	if e.Key != "" {
		if r := u.HashtagRubric(e.Key); r != "" {
			rubric = r
		}
	}
	return jx.M{"rubric": rubric, "rubric_default": rubricDefault, "entities": ents, "synonyms": syns}
}

// TopicSetRubric — своя рубрика пользователя для темы дерева ("" — вернуть из словаря).
func (u *U) TopicSetRubric(id, rubric string) (*Topic, error) {
	e := u.TopicByID(id)
	if e == nil {
		return nil, Bad("Такой темы нет.")
	}
	if e.Key == "" {
		return nil, Bad("Рубрика есть только у тем из дерева.")
	}
	tag := strings.ReplaceAll(HashtagNorm(rubric), "_", "")
	if tag != "" && !HashtagValid(tag) {
		return nil, Bad("Рубрика — одно слово из букв и цифр, от 2 до %d символов.", HashtagMaxLen)
	}
	s := u.settingsRaw(u.Ctx, u.ID)
	rub := jx.Map(s, "hashtag_rubrics")
	if rub == nil {
		rub = jx.M{}
	}
	def := ""
	for _, t := range HashtagsForTopic(e.Key) {
		if hashtagDictionary().tags[t].Kind == "rubric" {
			def = t
			break
		}
	}
	if tag == "" || tag == def {
		delete(rub, e.Key)
	} else {
		rub[e.Key] = tag
	}
	s["hashtag_rubrics"] = rub
	if _, err := u.DB.Pool.Exec(u.Ctx, `UPDATE users SET settings = $1 WHERE id = $2`, jx.Marshal(s), u.ID); err != nil {
		return nil, err
	}
	return e, nil
}

func jsonRoundTrip(src any, dst any) error { return json.Unmarshal(jx.Marshal(src), dst) }

var _ = context.Background
var _ = time.Now
