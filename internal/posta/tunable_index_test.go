package posta

import "testing"

func TestTunableRegistryIndex(t *testing.T) {
	if len(tunablesByKey) != len(Tunables) {
		t.Fatal("index and registry differ")
	}
	for i := range Tunables {
		if got := tunableByKey(Tunables[i].Key); got != &Tunables[i] {
			t.Fatalf("incorrect definition for %q", Tunables[i].Key)
		}
	}
	if tunableByKey("__unknown__") != nil {
		t.Fatal("unexpected unknown definition")
	}
}

func BenchmarkTunableByKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = tunableByKey("autopilot_model_topic_daily")
	}
}
