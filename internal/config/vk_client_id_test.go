package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVKClientIDLocalFileAndEnvironment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("VK_CLIENT_ID", "")
	path := filepath.Join(root, "vk-client-id")
	if err := os.WriteFile(path, []byte("54839247\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := vkClientID(root); got != "54839247" {
		t.Fatalf("file: %q", got)
	}
	t.Setenv("VK_CLIENT_ID", "12345")
	if got := vkClientID(root); got != "12345" {
		t.Fatalf("environment must take precedence: %q", got)
	}
	t.Setenv("VK_CLIENT_ID", "")
	if err := os.WriteFile(path, []byte("not-an-id"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := vkClientID(root); got != "" {
		t.Fatalf("invalid id: %q", got)
	}
}
