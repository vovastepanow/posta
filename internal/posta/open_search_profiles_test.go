package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

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

func TestOpenSettingsPreserveLiteralIntent(t *testing.T) {
	s := normalizedSettingsForMode(DefaultSettings(), jx.M{"topic":"C++", "topic2":"C#", "query":"C++ ? без рекламы"}, true)
	if jx.Str(s,"topic") != "C++" || jx.Str(s,"topic2") != "C#" || jx.Str(s,"query") != "C++ ? без рекламы" {
		t.Fatalf("search intent lost: %v", s)
	}
	long := strings.Repeat("a", 200)
	if literalIntentKey(long+" C++") == literalIntentKey(long+" C#") {
		t.Fatal("compound key was truncated")
	}
}

func TestLiteralIndexKeepsPunctuationDistinct(t *testing.T) {
	a,b := &Topic{ID:"first",Title:"C++"}, &Topic{ID:"second",Title:"C#"}
	idx:=buildTopicIndex([]*Topic{a,b})
	if idx.byLiteral["c++"]!=a || idx.byLiteral["c#"]!=b || idx.byLiteral["c"]!=nil {
		t.Fatal("literal lookup inherited a taxonomy alias")
	}
}
