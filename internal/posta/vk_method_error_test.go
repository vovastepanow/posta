package posta

import (
	"errors"
	"strings"
	"testing"
)

func TestVKMethodErrorExplainsGroupPhotoRestriction(t *testing.T) {
	got := vkMethodError("photos.getWallUploadServer", 27, "Group authorization failed").Error()
	if !strings.Contains(got, "фотографии") || !strings.Contains(got, "ключом сообщества") {
		t.Fatalf("missing actionable explanation: %s", got)
	}
	var photoErr *vkPhotoAuthError
	if !errors.As(vkMethodError("photos.getWallUploadServer", 27, "Group authorization failed"), &photoErr) {
		t.Fatal("photo authorization restriction must be identifiable for retry")
	}
	if errors.As(vkMethodError("wall.post", 27, "Group authorization failed"), &photoErr) {
		t.Fatal("wall posting failure must not trigger photo retry")
	}
	got = vkMethodError("wall.post", 27, "Group authorization failed").Error()
	if !strings.Contains(got, "wall.post") || strings.Contains(got, "загружать фотографии") {
		t.Fatalf("misidentified failing method: %s", got)
	}
}
