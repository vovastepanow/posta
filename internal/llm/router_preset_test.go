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

func TestNormalizeMigratesOnlyLegacyFreeDefaults(t *testing.T) {
	s := Preset("free")
	s.Roles[RolePrescreen] = RoleConfig{Primary: LocalFast, Fallback: Off}
	s.Roles[RoleAnalysis] = RoleConfig{Primary: FreeVision, Fallback: LocalSmart, Threshold: 71}
	custom := Target{"openrouter", "custom:free"}
	s.Roles[RoleSmart] = RoleConfig{Primary: custom, Fallback: LocalSmart}
	n := s.Normalize()
	if !n.Roles[RolePrescreen].Primary.Empty() || !n.Roles[RoleAnalysis].Fallback.Empty() ||
		n.Roles[RoleAnalysis].Threshold != 71 || n.Roles[RoleSmart].Primary != custom ||
		n.Roles[RoleSmart].Fallback != LocalSmart {
		t.Fatal("legacy defaults or custom settings mishandled")
	}
	if s.Roles[RoleAnalysis].Fallback != LocalSmart {
		t.Fatal("normalization mutated the original map")
	}
}
