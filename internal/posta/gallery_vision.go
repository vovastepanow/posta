package posta

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"posta/internal/config"
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
	if mixedAlbumEvidenceDescription(item) != "" {
		return "mixed_album"
	}
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

// Each album member contributes evidence in source order. Two sampled frames
// per video bound the visual model input; audio is handled by classification.
func mixedAlbumVisionFrames(gallery []jx.M, image func(jx.M) []byte, video func(jx.M) [][]byte) ([][]byte, []int, bool) {
	if len(gallery) < 2 || len(gallery) > 10 {
		return nil, nil, false
	}
	var frames [][]byte
	var counts []int
	for _, file := range gallery {
		var sample [][]byte
		if jx.Str(file, "video") != "" {
			sample = video(file)
			if len(sample) > 2 {
				sample = [][]byte{sample[0], sample[len(sample)-1]}
			}
		} else if b := image(file); len(b) > 0 {
			sample = [][]byte{b}
		}
		if len(sample) == 0 {
			return nil, nil, false
		}
		for _, frame := range sample {
			if len(frame) == 0 || imgx.Validate(frame) != nil {
				return nil, nil, false
			}
		}
		frames = append(frames, sample...)
		counts = append(counts, len(sample))
	}
	return frames, counts, true
}

func mixedAlbumEvidenceDescription(item jx.M) string {
	gallery := ContentGallery(item)
	counts, _ := item["_album_frame_counts"].([]int)
	if len(gallery) < 2 || len(counts) != len(gallery) {
		return ""
	}
	var parts []string
	start := 1
	for i, file := range gallery {
		n := counts[i]
		if n < 1 || n > 2 {
			return ""
		}
		kind := "фото"
		if jx.Str(file, "video") != "" {
			kind = "выборочные кадры отдельного видео без звука"
		}
		parts = append(parts, fmt.Sprintf("вложение %d: %s (изображения %d–%d)", i+1, kind, start, start+n-1))
		start += n
	}
	return "Одна публикация с несколькими вложениями в исходном порядке: " + strings.Join(parts, "; ") + ". Не воспринимай кадры разных роликов как одно видео, не додумывай звук или промежуточные события."
}

// A cached video path is never a temporary file: do not remove it at the
// end of analysis. Downloads for new album members are cached individually.
func (u *U) prepareMixedAlbumVision(item jx.M, poster []byte) (jx.M, [][]byte, map[string][]byte, bool) {
	gallery := ContentGallery(item)
	loaded := map[string][]byte{}
	frames, counts, ok := mixedAlbumVisionFrames(gallery, func(file jx.M) []byte {
		image := jx.Str(file, "image")
		if image == ContentImageKey(item) {
			loaded[image] = poster
			return poster
		}
		if local := u.MediaExisting(jx.Str(file, "image_local")); local != "" {
			image = local
		}
		b := u.discoveryImageBytes(image)
		loaded[jx.Str(file, "image")] = b
		return b
	}, func(file jx.M) [][]byte {
		raw := jx.Str(file, "video")
		local := u.MediaExisting(jx.Str(file, "video_local"))
		if local == "" {
			local = u.MediaExisting(raw)
		}
		path := u.MediaPath(local)
		if path == "" {
			if !strings.HasPrefix(raw, "https://") {
				return nil
			}
			path = u.DownloadVideoTemp(raw, config.VideoMaxBytes, nil)
			if path == "" {
				return nil
			}
			defer os.Remove(path)
		}
		board := u.videoStoryboardContext(u.Ctx, path, 2)
		if board == nil || len(board.Frames) == 0 {
			return nil
		}
		if local == "" {
			local = u.CacheDownloadedVideo(raw, path)
		}
		if local == "" {
			return nil
		}
		file["video_local"] = local
		mediaProtect.Store(filepath.Base(local), time.Now())
		return board.Frames
	})
	if !ok {
		return item, nil, nil, false
	}
	out := jx.Copy(item)
	out["gallery"], out["_album_frame_counts"] = gallery, counts
	return out, frames, loaded, true
}
