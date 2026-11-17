package posta

import (
	"context"
	"os"
	"path/filepath"
	"posta/internal/llm"
	"testing"
)

func TestAuditResumesWithoutChangingSnapshotOrRepeatingFinishedRows(t *testing.T) {
	dir := t.TempDir()
	snapshot := []byte("{\"store\":\"pool\",\"key\":\"1\",\"data\":{\"text\":\"private payload\"},\"tags\":[]}\n")
	review := []byte("{\"store\":\"pool\",\"key\":\"1\",\"status\":\"agrees\"}\n")
	if err := os.WriteFile(filepath.Join(dir, "snapshot.jsonl"), snapshot, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "review.jsonl"), review, 0600); err != nil {
		t.Fatal(err)
	}
	a := &App{LLM: llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("http://127.0.0.1:1", "test", "", ""))}
	a.LLM.OnCall = func(llm.Call) { t.Error("finished row sent again") }
	if err := a.AuditClassifications(context.Background(), dir, false, 25, func(string) {}); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "snapshot.jsonl"))
	if string(got) != string(snapshot) {
		t.Fatal("snapshot changed")
	}
	got, _ = os.ReadFile(filepath.Join(dir, "review.jsonl"))
	if string(got) != string(review) {
		t.Fatal("finished row duplicated")
	}
}

func TestAuditRejectsDamagedSnapshot(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "snapshot.jsonl"), []byte(`{"store":"pool"`), 0600)
	if _, err := (&App{}).classificationSnapshot(context.Background(), dir); err == nil {
		t.Fatal("partial snapshot accepted")
	}
}

func TestAuditOrderIncludesBothStoresAndPreservesAllRecords(t *testing.T) {
	in := []auditRecord{{Store: "pool", Key: "1"}, {Store: "pool", Key: "2"}, {Store: "public", Key: "3"}, {Store: "public", Key: "4"}}
	got := diverseAuditOrder(in)
	if len(got) != 4 || got[0].Store != "pool" || got[1].Store != "public" {
		t.Fatal(got)
	}
	seen := map[string]bool{}
	for _, r := range got {
		if seen[r.Key] {
			t.Fatal("duplicate")
		}
		seen[r.Key] = true
	}
}
