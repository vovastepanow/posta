package posta

import "testing"

func TestAutopilotSearchIntentIdentity(t *testing.T) {
	for _, pair := range [][2]string{
		{"C", "C++"},
		{"C++", "C#"},
		{"SMM", "SMM и блогинг"},
		{"маркетинг", "Маркетинг и реклама"},
		{"мемы про котов", "мемы про котов без мата"},
		{"крипта", "Криптовалюта"},
	} {
		if autopilotTopicKey(pair[0]) == autopilotTopicKey(pair[1]) {
			t.Fatalf("different search requests merged: %q, %q", pair[0], pair[1])
		}
	}
	if autopilotTopicKey(" SMM  ") != autopilotTopicKey("smm") {
		t.Fatal("case and whitespace split identical requests")
	}
	if autopilotCanonicalTopic("  мемы   про котов без мата ") != "мемы про котов без мата" {
		t.Fatal("search intent changed")
	}
}

func BenchmarkAutopilotTopicKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = autopilotTopicKey("SMM и блогинг")
	}
}
