package jx

import (
	"reflect"
	"testing"
)

func TestTypedListConversions(t *testing.T) {
	m := M{"key": "value"}
	typed := []M{m, nil}
	got := ToMaps(typed)
	if !reflect.DeepEqual(got, typed) {
		t.Fatalf("typed maps: %v", got)
	}
	got[0] = nil
	if typed[0] == nil {
		t.Fatal("ToMaps must not alias the input slice")
	}
	if !reflect.DeepEqual(ToMaps([]any{1, m, "skip", M(nil)}), typed) {
		t.Fatal("mixed maps were not filtered")
	}
	for _, v := range []any{nil, []M{}, []any{}, []string{"skip"}} {
		if ToMaps(v) != nil {
			t.Fatalf("expected nil maps for %T", v)
		}
	}
	if !reflect.DeepEqual(ToStrs([]any{1, "", m, "value"}), []string{"", "value"}) {
		t.Fatal("mixed strings were not filtered")
	}
	if ToStrs([]M{m}) != nil || ToStrs([]any{}) != nil {
		t.Fatal("expected nil for lists without strings")
	}
	if s := ToStrs([]string{}); s == nil || len(s) != 0 {
		t.Fatal("typed empty string slice must remain non-nil")
	}
}
