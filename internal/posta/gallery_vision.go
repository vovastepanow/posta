package posta

import (
	"posta/internal/imgx"
	"posta/internal/jx"
)

// The same model call can inspect three representative images, rather than
// the first attachment alone. Bound network work to two additional fetches.
func galleryVisionFrames(item jx.M, poster []byte, fetch func(string) []byte) ([][]byte, map[string][]byte) {
	frames := [][]byte{poster}
	if PostItemHasVideo(item) || jx.Str(item, "video_via") != "" {
		return frames, nil
	}
	seen := map[string]bool{ContentImageKey(item): true}
	var others []string
	for _, f := range ContentGallery(item) {
		image := jx.Str(f, "image")
		if image == "" || seen[image] {
			continue
		}
		seen[image] = true
		others = append(others, image)
	}
	if len(others) > 2 {
		others = []string{others[0], others[len(others)-1]}
	}
	loaded := map[string][]byte{}
	for _, image := range others {
		if b := fetch(image); len(b) > 0 && imgx.Validate(b) == nil {
			frames = append(frames, b)
			loaded[image] = b
		}
	}
	return frames, loaded
}

func visualEvidenceKind(item jx.M, count int, video *Storyboard) string {
	if video != nil {
		return "storyboard"
	}
	if PostItemHasVideo(item) || jx.Str(item, "video_via") != "" {
		return "poster_only"
	}
	if total := len(ContentGallery(item)); total > 1 {
		if count < total {
			return "album_partial"
		}
		return "album"
	}
	return "image"
}
