package posta

import "testing"

func TestTelegramAttachmentCombinations(t *testing.T) {
	for _, tc := range []struct {
		kinds []string
		ok    bool
	}{
		{[]string{"audio"}, true},
		{[]string{"audio", "audio"}, true},
		{[]string{"document", "document"}, true},
		{[]string{"audio", "photo"}, false},
		{[]string{"document", "audio"}, false},
		{[]string{"photo", "video"}, true},
		{[]string{"unknown"}, false},
	} {
		var files []Prepared
		for _, kind := range tc.kinds {
			files = append(files, Prepared{Kind: kind})
		}
		if err := telegramAttachmentCheck(files); (err == nil) != tc.ok {
			t.Errorf("%v: %v", tc.kinds, err)
		}
	}
}
