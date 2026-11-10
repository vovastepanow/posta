package posta

import (
	"strings"
	"testing"
)

func TestPromptTemplates(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range promptTemplates {
		if p.ID == "" || seen[p.ID] {
			t.Fatalf("missing or duplicate prompt ID %q", p.ID)
		}
		seen[p.ID] = true
		if err := validatePrompt(p, p.Default); err != nil {
			t.Errorf("%s: %v", p.ID, err)
		}
		for _, v := range p.Variables {
			if err := validatePrompt(p, strings.Replace(p.Default, "{{"+v+"}}", "", 1)); err == nil {
				t.Errorf("%s: missing %s accepted", p.ID, v)
			}
		}
	}
}

func TestPromptValuesAreNotReexpanded(t *testing.T) {
	got := renderPrompt("{{text}} {{topic}}", map[string]string{"text": "{{topic}}", "topic": "example"})
	if got != "{{topic}} example" {
		t.Fatalf("unexpected expansion: %q", got)
	}
}

func TestPromptRejectsUnknownVariable(t *testing.T) {
	p := PromptTemplate{Variables: []string{"text"}}
	if validatePrompt(p, "{{text}} {{typo}}") == nil {
		t.Fatal("unknown variable accepted")
	}
}
