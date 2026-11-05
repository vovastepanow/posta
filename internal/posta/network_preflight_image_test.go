package posta

import "testing"

func TestVKPreflightAcceptsEditorImages(t *testing.T) {
	a := &App{}
	kept, skipped, err := a.NetworkPreflight("vk", "", []string{"image"})
	if err != nil || kept != 1 || len(skipped) != 0 {
		t.Fatalf("VK image preflight: kept=%d skipped=%v err=%v", kept, skipped, err)
	}
	_, skipped, err = a.NetworkPreflight("vk", "текст", []string{"video"})
	if err != nil || len(skipped) != 1 {
		t.Fatalf("VK video preflight: skipped=%v err=%v", skipped, err)
	}
}
