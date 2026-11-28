package posta

import "testing"

func TestOpenSearchProfileIdentity(t *testing.T) {
	for _, pair := range [][2]string{
		{"C++", "C#"}, {"C#", "C"},
		{"Мемы про котов", "Мемы про котов без мата"},
		{"SMM", "SMM и блогинг"},
	} {
		if openSearchProfileKey(pair[0]) == openSearchProfileKey(pair[1]) || openSearchProfileID(pair[0]) == openSearchProfileID(pair[1]) {
			t.Fatalf("different intents merged: %q and %q", pair[0], pair[1])
		}
	}
	if openSearchProfileID("  SMM  ") != openSearchProfileID("smm") {
		t.Fatal("case and whitespace should not split identical queries")
	}
	query := "Практические советы по продвижению стоматологических клиник в Telegram без рекламы"
	entry := NormalizeTopicEntry(&Topic{ID: openSearchProfileID(query), Title: query})
	if entry.Title != query || entry.Parent != nil {
		t.Fatalf("query was truncated or assigned a parent: %+v", entry)
	}
}

func TestExactSearchProfileWinsOverLegacyAlias(t *testing.T) {
	legacy := &Topic{ID: "legacy", Title: "Programming", Aliases: []string{"C++"}}
	exact := &Topic{ID: openSearchProfileID("C++"), Title: "C++"}
	other := &Topic{ID: openSearchProfileID("C#"), Title: "C#"}
	a := &App{catalog: &Catalog{idx: buildTopicIndex([]*Topic{legacy, other, exact})}}
	if a.TopicLookup("C++") != exact || a.TopicLookup("C#") != other {
		t.Fatal("legacy normalization swallowed exact query")
	}
}
