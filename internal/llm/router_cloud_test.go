package llm

import "testing"

func TestCloudPresetDoesNotUseLocalModels(t *testing.T) {
	s := Preset("cloud")
	for _, role := range Roles {
		rc, ok := s.Roles[role]
		if !ok {
			t.Fatalf("missing role %s", role)
		}
		if rc.Primary.Provider == "ollama" || rc.Fallback.Provider == "ollama" {
			t.Errorf("role %s uses Ollama: %+v", role, rc)
		}
		if role != RoleEmbed && rc.Primary.Provider != "openrouter" {
			t.Errorf("role %s is not routed to OpenRouter: %+v", role, rc)
		}
	}
	if !s.Roles[RoleEmbed].Primary.Empty() {
		t.Error("cloud preset should leave embeddings disabled")
	}
}
