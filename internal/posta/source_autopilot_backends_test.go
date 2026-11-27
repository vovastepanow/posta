package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAutopilotBackendRegistry(t *testing.T) {
	names, settings := map[string]bool{}, map[string]bool{}
	for _, backend := range autopilotSearchRegistry {
		if backend.name == "" || backend.setting == "" || backend.search == nil ||
			names[backend.name] || settings[backend.setting] || tunableByKey(backend.setting) == nil {
			t.Fatalf("invalid or duplicate backend: %s (%s)", backend.name, backend.setting)
		}
		names[backend.name], settings[backend.setting] = true, true
	}
	seen := map[string]bool{}
	status := autopilotBackendStatus(func(key string) bool { return key == "search_rutube" })
	for _, row := range status {
		seen[jx.Str(row, "name")] = jx.Bool(row, "enabled")
	}
	if len(status) != len(names) || !seen["Rutube"] || seen["YouTube"] {
		t.Fatalf("incorrect public backend status: %v", status)
	}
}
