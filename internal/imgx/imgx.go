// Package imgx — картинки: проверка, уменьшение в JPEG, перцептивный хэш, листы раскадровки.
package imgx

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"

	_ "golang.org/x/image/bmp"
	xdraw "golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

const MaxImageBytes = 10 << 20

var allowed = map[string]bool{"jpeg": true, "png": true, "gif": true, "webp": true, "bmp": true}

// Validate — JPEG, PNG, GIF, WebP или BMP до 10 МБ и до 40 Мп.
func Validate(b []byte) error {
	if len(b) == 0 || len(b) > MaxImageBytes {
		return errors.New("Изображение должно быть не больше 10 МБ.")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil || !allowed[format] {
		return errors.New("Поддерживаются только JPEG, PNG, GIF, WebP и BMP.")
	}
	if cfg.Width*cfg.Height > 40_000_000 {
		return errors.New("Слишком большое разрешение изображения.")
	}
	return nil
}

// Size — ширина и высота без полного декодирования.
func Size(b []byte) (int, int, bool) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, false
	}
	return cfg.Width, cfg.Height, true
}

func Decode(b []byte) (image.Image, error) {
	img, _, err := image.Decode(bytes.NewReader(b))
	return img, err
}

// Shrink — JPEG не больше maxSide по длинной стороне, белый фон под прозрачностью. Не вышло или не нужно — исходные байты.
func Shrink(b []byte, maxSide, quality int, force bool) []byte {
	w, h, ok := Size(b)
	if !ok || (!force && max(w, h) <= maxSide) {
		return b
	}
	src, err := Decode(b)
	if err != nil {
		return b
	}
	out := ResizeImage(src, maxSide)
	var buf bytes.Buffer
	if jpeg.Encode(&buf, out, &jpeg.Options{Quality: quality}) != nil || buf.Len() == 0 {
		return b
	}
	return buf.Bytes()
}

// ResizeImage — уменьшение до maxSide (не увеличивает) на белом фоне.
func ResizeImage(src image.Image, maxSide int) *image.RGBA {
	sb := src.Bounds()
	scale := min(1.0, float64(maxSide)/float64(max(sb.Dx(), sb.Dy())))
	w, h := max(1, int(float64(sb.Dx())*scale+0.5)), max(1, int(float64(sb.Dy())*scale+0.5))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.White}, image.Point{}, draw.Src)
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, sb, draw.Over, nil)
	return dst
}

func EncodeJPEG(img image.Image, quality int) []byte {
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality})
	return buf.Bytes()
}
