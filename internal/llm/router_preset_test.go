package llm

import "testing"

func TestFreePresetDoesNotRequireLocalModels(t *testing.T) {
	s := Preset("free")
	for role, cfg := range s.Roles {
		for _, model := range []Target{cfg.Primary, cfg.Fallback} {
			if !model.Empty() && (model.Provider != "openrouter" || !model.Free()) {
				t.Fatalf("free preset role %s requires %v", role, model)
			}
		}
	}
	if !s.Roles[RolePrescreen].Primary.Empty() {
		t.Fatal("duplicate generative prescreen enabled")
	}
	if Preset("local").Roles[RoleAnalysis].Primary.Provider != "ollama" {
		t.Fatal("explicit local preset no longer works")
	}
}
