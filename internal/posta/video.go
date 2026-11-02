package posta

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"posta/internal/config"
	"posta/internal/imgx"
)

func (a *App) ffprobe(path string, args ...string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, _ := exec.CommandContext(ctx, a.Cfg.FFprobe, append(append([]string{"-v", "error"}, args...), path)...).Output()
	return strings.TrimSpace(string(out))
}

func (a *App) HasFFmpeg() bool {
	_, err := exec.LookPath(a.Cfg.FFmpeg)
	return err == nil
}

// FFmpegRun — ffmpeg с аргументами; true — файл out получился.
func (a *App) FFmpegRun(args []string, out string, timeout time.Duration) bool {
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.Cfg.FFmpeg, append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if cmd.Run() != nil {
		return false
	}
	st, err := os.Stat(out)
	return err == nil && st.Size() > 0
}

// FFmpegRunProgress — как FFmpegRun, но сообщает долю готового (0–99) по out_time_us и длительности.
func (a *App) FFmpegRunProgress(args []string, out string, timeout time.Duration, duration float64, onProgress func(p *float64)) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, a.Cfg.FFmpeg, append(append([]string{"-v", "error", "-y", "-progress", "pipe:1", "-nostats"}, args...), out)...)
	stdout, err := cmd.StdoutPipe()
	if err != nil || cmd.Start() != nil {
		return false
	}
	if duration <= 0 {
		onProgress(nil)
	}
	sc := bufio.NewScanner(stdout)
	for sc.Scan() {
		line := sc.Text()
		if duration > 0 && strings.HasPrefix(line, "out_time_us=") {
			us, _ := strconv.ParseFloat(line[12:], 64)
			p := math.Min(99, math.Max(0, us/1e6/duration*100))
			onProgress(&p)
		}
	}
	if cmd.Wait() != nil {
		return false
	}
	st, err := os.Stat(out)
	return err == nil && st.Size() > 0
}

// DownloadVideoTemp — ролик во временный файл (до maxBytes); "" — не вышло. Файл удаляет вызывающий.
func (a *App) DownloadVideoTemp(raw string, maxBytes int64, headers map[string]string) string {
	if strings.HasPrefix(raw, FourchanImg+"/") {
		HostThrottle("i.4cdn.org", FourchanMediaGap)
		if len(headers) == 0 {
			headers = FourchanMediaHeaders(raw)
		}
	}
	f, err := os.CreateTemp("", "ct-video-")
	if err != nil {
		return ""
	}
	path := f.Name()
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		f.Close()
		os.Remove(path)
		return ""
	}
	req.Header.Set("User-Agent", HTTPUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := defaultClient.Do(req)
	ok := false
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == 200 {
			n, cerr := io.Copy(f, io.LimitReader(resp.Body, maxBytes+1))
			ok = cerr == nil && n > 0 && n <= maxBytes
		}
	}
	f.Close()
	if !ok {
		os.Remove(path)
		return ""
	}
	return path
}

type Storyboard struct {
	Frames     [][]byte
	Timestamps []float64
	Duration   float64
	File       string // скачанный ролик (удаляет вызывающий)
}

// VideoStoryboard — кадры в серединах равных отрезков (~раз в VideoSecondsPerFrame с), уменьшенные до VideoFrameMaxSide.
func (a *App) VideoStoryboard(path string) *Storyboard {
	if !a.HasFFmpeg() || !fileExists(path) {
		return nil
	}
	duration, _ := strconv.ParseFloat(a.ffprobe(path, "-show_entries", "format=duration", "-of", "csv=p=0"), 64)
	duration = math.Max(0, duration)
	span := duration
	if span <= 0 {
		span = config.VideoFramesMin * config.VideoSecondsPerFrame
	}
	count := max(config.VideoFramesMin, min(config.VideoFramesMax, int(math.Round(span/config.VideoSecondsPerFrame))))
	s := config.VideoFrameMaxSide
	sb := &Storyboard{Duration: duration}
	for i := 0; i < count; i++ {
		at := math.Round(span*(float64(i)+0.5)/float64(count)*100) / 100
		out, err := os.CreateTemp("", "ct-frame-")
		if err != nil {
			break
		}
		out.Close()
		scale := fmt.Sprintf("scale='if(gt(iw,ih),%d,-2)':'if(gt(iw,ih),-2,%d)'", s, s)
		if a.FFmpegRun([]string{"-ss", fmt.Sprintf("%.2f", at), "-i", path, "-frames:v", "1", "-vf", scale, "-q:v", "4", "-f", "image2"}, out.Name(), time.Minute) {
			if b, err := os.ReadFile(out.Name()); err == nil {
				sb.Frames = append(sb.Frames, b)
				sb.Timestamps = append(sb.Timestamps, at)
			}
		}
		os.Remove(out.Name())
	}
	if len(sb.Frames) == 0 {
		return nil
	}
	return sb
}

// VideoAnalysisInput — видео для модели; не вышло — по превью.
func (u *U) VideoAnalysisInput(raw string, poster []byte) *Storyboard {
	posterOnly := &Storyboard{Frames: [][]byte{poster}}
	if !u.HasFFmpeg() {
		u.Log("-> ffmpeg не найден на сервере, кадры видео не извлекаются; анализирую превью.")
		return posterOnly
	}
	path := u.DownloadVideoTemp(raw, config.VideoMaxBytes, nil)
	if path == "" {
		u.Log("-> Видео не скачалось (размер или ошибка сети), анализирую превью.")
		return posterOnly
	}
	board := u.VideoStoryboard(path)
	if board == nil {
		u.Log("-> Кадры видео не извлечены, анализирую превью.")
		posterOnly.File = path
		return posterOnly
	}
	u.Log("-> Раскадровка: %d кадров из видео длиной %s.", len(board.Frames), FormatVideoTime(board.Duration))
	board.File = path
	return board
}

// Mp4HasFastStart — индекс moov в начале MP4: браузер играет, не скачав файл целиком.
func Mp4HasFastStart(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()
	st, _ := f.Stat()
	size := st.Size()
	head := make([]byte, 16)
	for off := int64(0); off+8 <= size; {
		n, _ := f.ReadAt(head, off)
		if n < 8 {
			break
		}
		typ := string(head[4:8])
		if typ == "moov" || typ == "mdat" {
			return typ == "moov"
		}
		atom := int64(binary.BigEndian.Uint32(head[:4]))
		if atom == 1 && n >= 16 {
			atom = int64(binary.BigEndian.Uint64(head[8:16]))
		}
		if atom < 8 {
			break
		}
		off += atom
	}
	return true
}

// BrowserPlayableVideo — ролик, который играют все браузеры (H.264), не тяжелее VideoLightBytes и не выше 720p, с быстрым стартом.
// Возвращает исходный путь или новый временный файл.
func (u *U) BrowserPlayableVideo(path string) string {
	if !u.HasFFmpeg() || !fileExists(path) {
		return path
	}
	codec := strings.ToLower(u.ffprobe(path, "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0"))
	unplayable := codec != "" && !contains([]string{"h264", "vp8", "vp9", "av1"}, codec)
	minSide := 0
	for _, s := range strings.Split(u.ffprobe(path, "-select_streams", "v:0", "-show_entries", "stream=width,height", "-of", "csv=p=0"), ",") {
		if n, err := strconv.Atoi(strings.TrimSpace(s)); err == nil && n > 0 && (minSide == 0 || n < minSide) {
			minSide = n
		}
	}
	st, _ := os.Stat(path)
	heavy := st.Size() > config.VideoLightBytes || minSide > config.VideoLightHeight
	slow := false
	if f, err := os.Open(path); err == nil {
		b := make([]byte, 8)
		f.Read(b)
		f.Close()
		slow = string(b[4:8]) == "ftyp" && !Mp4HasFastStart(path)
	}
	if !unplayable && !heavy && !slow {
		return path
	}
	tmp, err := os.CreateTemp("", "ct-h264-")
	if err != nil {
		return path
	}
	tmp.Close()
	out := tmp.Name()
	var ok bool
	if unplayable || heavy {
		if unplayable {
			u.Log("-> Видео в кодеке %s: браузер его может не воспроизвести — перекодирую в H.264 до %dp...", codec, config.VideoLightHeight)
		} else {
			u.Log("-> Видео тяжёлое (%s МБ) — перекодирую в H.264 до %dp...", fmtMB(st.Size()), config.VideoLightHeight)
		}
		h := config.VideoLightHeight
		ok = u.FFmpegRun([]string{"-i", path, "-map", "0:v:0", "-map", "0:a:0?", "-vf", fmt.Sprintf("scale='if(gt(iw,ih),-2,min(%d,iw))':'if(gt(iw,ih),min(%d,ih),-2)'", h, h),
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "27", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-f", "mp4"}, out, 10*time.Minute)
	} else {
		ok = u.FFmpegRun([]string{"-i", path, "-map", "0", "-c", "copy", "-movflags", "+faststart", "-f", "mp4"}, out, 5*time.Minute)
	}
	if ok {
		if os2, err := os.Stat(out); err == nil && !(heavy && !unplayable && os2.Size() >= st.Size()) {
			return out
		}
	}
	os.Remove(out)
	if unplayable {
		u.Log("-> Перекодировать не удалось, сохраняю как есть.")
	}
	return path
}

func FormatVideoTime(seconds float64) string {
	seconds = math.Max(0, seconds)
	return fmt.Sprintf("%d:%04.1f", int(seconds)/60, math.Mod(seconds, 60))
}

// StoryboardSheets — кадры видео сеткой 2×2 на листе с номером в углу: лист стоит как одна картинка.
func StoryboardSheets(frames [][]byte) [][]byte {
	if len(frames) < 2 {
		return frames
	}
	var sheets [][]byte
	per := config.StoryboardFramesPerSheet
	for ci := 0; ci*per < len(frames); ci++ {
		chunk := frames[ci*per : min(len(frames), (ci+1)*per)]
		var imgs []image.Image
		for _, b := range chunk {
			img, err := imgx.Decode(b)
			if err != nil {
				return frames
			}
			imgs = append(imgs, img)
		}
		cw, chh := 0, 0
		for _, im := range imgs {
			cw, chh = max(cw, im.Bounds().Dx()), max(chh, im.Bounds().Dy())
		}
		cols := 2
		if len(imgs) == 1 {
			cols = 1
		}
		rows := (len(imgs) + cols - 1) / cols
		sheet := image.NewRGBA(image.Rect(0, 0, cols*cw+(cols-1)*4, rows*chh+(rows-1)*4))
		draw.Draw(sheet, sheet.Bounds(), &image.Uniform{color.Black}, image.Point{}, draw.Src)
		for i, im := range imgs {
			x, y := (i%cols)*(cw+4), (i/cols)*(chh+4)
			b := im.Bounds()
			off := image.Pt(x+(cw-b.Dx())/2, y+(chh-b.Dy())/2)
			draw.Draw(sheet, image.Rectangle{off, off.Add(b.Size())}, im, b.Min, draw.Src)
			label := strconv.Itoa(ci*per + i + 1)
			draw.Draw(sheet, image.Rect(x, y, x+10+9*len(label), y+20), &image.Uniform{color.Black}, image.Point{}, draw.Src)
			d := &font.Drawer{Dst: sheet, Src: image.White, Face: basicfont.Face7x13, Dot: fixed.P(x+5, y+15)}
			d.DrawString(label)
		}
		sheets = append(sheets, imgx.EncodeJPEG(sheet, 88))
	}
	return sheets
}
