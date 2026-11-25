package posta

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestGalleryAnalysisUsesBoundedRepresentativeImages(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 256, 256))); err != nil {
		t.Fatal(err)
	}
	item := jx.M{"image": "one", "gallery": []jx.M{{"image": "one"}, {"image": "two"}, {"image": "three"}, {"image": "four"}}}
	var fetched []string
	frames, loaded := galleryVisionFrames(item, b.Bytes(), func(url string) []byte {
		fetched = append(fetched, url)
		return b.Bytes()
	})
	if len(frames) != 3 || len(loaded) != 2 || strings.Join(fetched, ",") != "two,four" {
		t.Fatalf("unexpected image sample: %v frames=%d", fetched, len(frames))
	}
	if visualEvidenceKind(item, len(frames), nil) != "album_partial" || !strings.Contains(visualEvidenceDescription(item, len(frames), nil), "3 из 4") {
		t.Fatal("partial sample was described as complete")
	}
	if collectorPrescreenComplete(item) {
		t.Fatal("album was rejected on first image alone")
	}
	video := jx.M{"image": "poster", "video": "https://example.org/video.mp4"}
	if collectorPrescreenComplete(video) || visualEvidenceKind(video, 1, nil) != "poster_only" || !strings.Contains(visualEvidenceDescription(video, 1, nil), "Само видео") {
		t.Fatal("cover was confused with actual video")
	}
	if !collectorPrescreenComplete(jx.M{"image": "photo"}) {
		t.Fatal("ordinary single image lost quick screening")
	}
}
