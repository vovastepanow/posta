package posta

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"testing"

	"posta/internal/config"
)

func TestJoyreactorImagePreservesYellowText(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	for y := 180; y < 190; y++ {
		for x := 40; x < 160; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 200, A: 255})
		}
	}
	var original bytes.Buffer
	if err := png.Encode(&original, img); err != nil {
		t.Fatal(err)
	}
	app := &App{Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "media_cache"}}
	local := app.MediaStoreImage("https://img1.joyreactor.cc/pics/example.png", original.Bytes())
	if local == "" {
		t.Fatal("image was not stored")
	}
	stored, err := os.ReadFile(app.MediaPath(local))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(stored, original.Bytes()) {
		t.Fatal("image was modified during storage")
	}
}

func TestJoyreactorFooterPreservesTextAbove(t *testing.T) {
	f, err := os.Open("testdata/joyreactor_footer.png")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	footer, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, footer.Bounds().Dx(), 400))
	for y := 365; y < 380; y++ {
		for x := 200; x < 600; x++ {
			img.Set(x, y, color.RGBA{R: 240, G: 200, A: 255})
		}
	}
	for y := 0; y < footer.Bounds().Dy(); y++ {
		for x := 0; x < footer.Bounds().Dx(); x++ {
			img.Set(x, 382+y, footer.At(x, y))
		}
	}
	var input bytes.Buffer
	if err := png.Encode(&input, img); err != nil {
		t.Fatal(err)
	}
	output := cropJoyreactorBadge(input.Bytes(), "png")
	if len(output) == 0 {
		t.Fatal("footer not recognised")
	}
	result, err := png.Decode(bytes.NewReader(output))
	if err != nil {
		t.Fatal(err)
	}
	if result.Bounds().Dy() != 382 {
		t.Fatalf("height = %d, want 382", result.Bounds().Dy())
	}
	if color.RGBAModel.Convert(result.At(300, 375)) != color.RGBAModel.Convert(img.At(300, 375)) {
		t.Fatal("yellow text was modified")
	}
}
