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
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"posta/internal/config"
	"posta/internal/imgx"
)

// Video processing is memory intensive; share the limit across all callers.
var ffmpegSlots = make(chan struct{}, 2)

func acquireFFmpeg(ctx context.Context) bool {
	select {
	case ffmpegSlots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func releaseFFmpeg() { <-ffmpegSlots }

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
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), timeout)
	defer cancel()
	if !acquireFFmpeg(ctx) {
		return false
	}
	defer releaseFFmpeg()
	cmd := exec.CommandContext(ctx, a.Cfg.FFmpeg, append(append([]string{"-v", "error", "-y"}, args...), out)...)
	if cmd.Run() != nil {
		return false
	}
	st, err := os.Stat(out)
	return err == nil && st.Size() > 0
}

// FFmpegRunProgress — как FFmpegRun, но сообщает долю готового (0–99) по out_time_us и длительности.
func (a *App) FFmpegRunProgress(args []string, out string, timeout time.Duration, duration float64, onProgress func(p *float64)) bool {
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), timeout)
	defer cancel()
	if !acquireFFmpeg(ctx) {
		return false
	}
	defer releaseFFmpeg()
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

// videoPosterFromPublicURL downloads a bounded public video and extracts a frame.
func (u *U) videoPosterFromPublicURL(raw string) []byte {
	if !u.HasFFmpeg() {
		return nil
	}
	f, err := os.CreateTemp("", "posta-poster-video-*")
	if err != nil {
		return nil
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)
	for hop := 0; hop <= 3; hop++ {
		r, err := u.FetchPublic(raw, FetchOpts{MaxBytes: config.VideoMaxBytes, Timeout: 90 * time.Second, Dest: path})
		if err != nil {
			return nil
		}
		if (r.Status == 301 || r.Status == 302 || r.Status == 303 || r.Status == 307 || r.Status == 308) && r.Location != "" {
			raw = RSSAbsoluteURL(raw, r.Location)
			continue
		}
		if !r.OK {
			return nil
		}
		board := u.videoStoryboard(path, 1)
		if board != nil && len(board.Frames) > 0 {
			return board.Frames[0]
		}
		return nil
	}
	return nil
}

// storyboardFrameCount applies the configured cap after choosing the sampling rate.
func storyboardFrameCount(duration float64, limit int) int {
	limit = max(1, min(config.VideoFramesMax, limit))
	if duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) {
		return min(limit, 4)
	}
	rate := 1.0
	if duration <= 15 {
		rate = 2
	}
	return max(1, min(limit, int(math.Ceil(duration*rate))))
}

var storyboardPTS = regexp.MustCompile(`pts_time:([-+0-9.eE]+)`)

// VideoStoryboard extracts evenly spaced frames in one FFmpeg pass.
func (a *App) VideoStoryboard(path string) *Storyboard {
	limit := config.VideoFramesMax
	if a.DB != nil {
		limit = a.TInt("video_frames")
	}
	return a.videoStoryboard(path, limit)
}

func (a *App) videoStoryboard(path string, limit int) *Storyboard {
	if !a.HasFFmpeg() || !fileExists(path) {
		return nil
	}
	duration, _ := strconv.ParseFloat(a.ffprobe(path, "-show_entries", "format=duration", "-of", "csv=p=0"), 64)
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration < 0 {
		duration = 0
	}
	count := storyboardFrameCount(duration, limit)
	span := duration
	if span <= 0 {
		span = float64(count)
	}
	dir, err := os.MkdirTemp("", "posta-storyboard-*")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	// Select the first frame at or after the midpoint of each equal interval.
	interval := span / float64(count)
	filter := fmt.Sprintf("select='gte(t,(selected_n+0.5)*%.9f)',showinfo,scale='if(gt(iw,ih),%d,-2)':'if(gt(iw,ih),-2,%d)'", interval, config.VideoFrameMaxSide, config.VideoFrameMaxSide)
	pattern := filepath.Join(dir, "frame-%03d.jpg")
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), 2*time.Minute)
	defer cancel()
	if !acquireFFmpeg(ctx) {
		return nil
	}
	output, err := exec.CommandContext(ctx, a.Cfg.FFmpeg, "-hide_banner", "-loglevel", "info", "-i", path, "-vf", filter, "-fps_mode", "vfr", "-frames:v", strconv.Itoa(count), "-q:v", "4", "-y", pattern).CombinedOutput()
	releaseFFmpeg()
	if err != nil {
		return nil
	}
	matches := storyboardPTS.FindAllStringSubmatch(string(output), -1)
	board := &Storyboard{Duration: duration}
	for i := 0; i < count; i++ {
		data, err := os.ReadFile(filepath.Join(dir, fmt.Sprintf("frame-%03d.jpg", i+1)))
		if err != nil {
			break
		}
		at := (float64(i) + 0.5) * interval
		if i < len(matches) {
			if parsed, err := strconv.ParseFloat(matches[i][1], 64); err == nil {
				at = parsed
			}
		}
		board.Frames = append(board.Frames, data)
		board.Timestamps = append(board.Timestamps, at)
	}
	if len(board.Frames) == 0 {
		return nil
	}
	return board
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

// VideoPageAnalysisInput samples a bounded public yt-dlp video for analysis.
// Failure leaves the poster usable, just as with a failed direct download.
func (u *U) VideoPageAnalysisInput(page string, poster []byte) (*Storyboard, func()) {
	fallback := &Storyboard{Frames: [][]byte{poster}}
	noop := func() {}
	if VideoPageVia(page) != "ytdlp" || !u.HasFFmpeg() {
		return fallback, noop
	}
	dir, err := os.MkdirTemp("", "posta-analysis-video-")
	if err != nil {
		return fallback, noop
	}
	cleanup := func() { os.RemoveAll(dir) }
	ctx, cancel := context.WithTimeout(u.Ctx, 40*time.Second)
	defer cancel()
	file, err := u.YtdlpDownload(ctx, page, YtdlpFormatFor(page), dir, config.VideoMaxBytes, nil)
	if err != nil {
		u.Log("-> Не удалось получить видео для анализа — оцениваю обложку: %s", err)
		return fallback, cleanup
	}
	board := u.VideoStoryboard(file)
	if board == nil {
		u.Log("-> Кадры не извлечены — оцениваю обложку.")
		return fallback, cleanup
	}
	u.Log("-> Раскадровка: %d кадров из видео длиной %s.", len(board.Frames), FormatVideoTime(board.Duration))
	return board, cleanup
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

// BrowserPlayableVideo prepares unsupported codecs and MP4 files without fast start.
// Compatible videos retain their original resolution: decoding belongs to the browser.
// Возвращает исходный путь или новый временный файл.
func (u *U) BrowserPlayableVideo(path string) string {
	if !u.HasFFmpeg() || !fileExists(path) {
		return path
	}
	codec := strings.ToLower(u.ffprobe(path, "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0"))
	unplayable := codec != "" && !contains([]string{"h264", "vp8", "vp9", "av1"}, codec)
	slow := false
	if f, err := os.Open(path); err == nil {
		b := make([]byte, 8)
		f.Read(b)
		f.Close()
		slow = string(b[4:8]) == "ftyp" && !Mp4HasFastStart(path)
	}
	if !unplayable && !slow {
		return path
	}
	tmp, err := os.CreateTemp("", "ct-h264-")
	if err != nil {
		return path
	}
	tmp.Close()
	out := tmp.Name()
	var ok bool
	if unplayable {
		u.Log("-> Видео в кодеке %s: браузер его может не воспроизвести — перекодирую в H.264 до %dp...", codec, config.VideoLightHeight)
		h := config.VideoLightHeight
		ok = u.FFmpegRun([]string{"-i", path, "-map", "0:v:0", "-map", "0:a:0?", "-vf", fmt.Sprintf("scale='if(gt(iw,ih),-2,min(%d,iw))':'if(gt(iw,ih),min(%d,ih),-2)'", h, h),
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "27", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "96k", "-movflags", "+faststart", "-f", "mp4"}, out, 10*time.Minute)
	} else {
		ok = u.FFmpegRun([]string{"-i", path, "-map", "0", "-c", "copy", "-movflags", "+faststart", "-f", "mp4"}, out, 5*time.Minute)
	}
	if ok {
		if _, err := os.Stat(out); err == nil {
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
