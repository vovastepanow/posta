package posta

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// yt-dlp (tools/bin/yt-dlp — самодостаточная сборка, Python в системе не нужен): списки и скачивание роликов TikTok, YouTube и X.
// JS-движок для YouTube — deno (tools/bin/deno). Очередь роликов — горутина сервера.

const (
	ytdlpListTimeout      = 30 * time.Second
	ytdlpDownloadTimeout  = 180 * time.Second
	ytdlpConvertTimeout   = 150 * time.Second
	ytdlpWatchMaxHeight   = 720
	ytdlpShortsMaxHeight  = 854
	ytdlpWatchMaxSeconds  = 600
	ytdlpWatchClipSeconds = 60 // длинный ролик YouTube: берётся только начало
	ytdlpQueueKey         = "ytdlp_queue"
	ytdlpQueueMax         = 500
	ytdlpTries            = 3
)

var ytdlpURLRe = regexp.MustCompile(`(?i)^https://[a-z0-9.-]+/[\x21-\x7e]*$`)

// YtdlpCheckURL — только https без пробелов и управляющих символов.
func YtdlpCheckURL(u string) (string, error) {
	if !ytdlpURLRe.MatchString(u) {
		return "", Bad("Некорректная ссылка для загрузки видео.")
	}
	return u, nil
}

type ExecResult struct {
	OK     bool
	Code   int
	Stdout string
	Stderr string
}

// Exec — внешняя программа: массив аргументов без оболочки, stdin закрыт, по таймауту — завершение. onLine — строки stdout по мере вывода.
func Exec(ctx context.Context, name string, args []string, timeout time.Duration, env []string, onLine func(string)) ExecResult {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	if env != nil {
		cmd.Env = env
	}
	var stdout, stderr bytes.Buffer
	cmd.Stderr = &stderr
	if onLine != nil {
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return ExecResult{Code: -1, Stderr: err.Error()}
		}
		if err := cmd.Start(); err != nil {
			return ExecResult{Code: -1, Stderr: "Не удалось запустить программу."}
		}
		sc := bufio.NewScanner(io.TeeReader(pipe, &stdout))
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			onLine(sc.Text())
		}
	} else {
		cmd.Stdout = &stdout
		if err := cmd.Start(); err != nil {
			return ExecResult{Code: -1, Stderr: "Не удалось запустить программу."}
		}
	}
	err := cmd.Wait()
	r := ExecResult{Stdout: stdout.String(), Stderr: stderr.String()}
	if ctx.Err() == context.DeadlineExceeded {
		r.Code, r.Stderr = -1, "Превышено время ожидания."
		return r
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.Code = ee.ExitCode()
		} else {
			r.Code = -1
		}
		return r
	}
	r.OK = true
	return r
}

func (a *App) ytdlpCacheDir() string {
	d := filepath.Join(a.Cfg.DataDir, "ytdlp-cache")
	os.MkdirAll(d, 0o775)
	return d
}

var progLineRe = regexp.MustCompile(`^PROG (\d+) (\S+) (\S+) (\S+)`)

// Limit all yt-dlp invocations, including playlist probes outside the video queue.
var ytdlpSlots = make(chan struct{}, 2)

// YtdlpRun — yt-dlp с общими аргументами проекта; адрес — после «--» у вызывающего.
func (a *App) YtdlpRun(ctx context.Context, args []string, timeout time.Duration, js bool, onProgress func(done, total int64, format string)) ExecResult {
	select {
	case ytdlpSlots <- struct{}{}:
		defer func() { <-ytdlpSlots }()
	case <-ctx.Done():
		return ExecResult{Code: -1, Stderr: ctx.Err().Error()}
	}
	cache := a.ytdlpCacheDir()
	base := []string{"--ignore-config", "--no-warnings"}
	if onProgress != nil {
		base = append(base, "--progress", "--newline", "--progress-template",
			"download:PROG %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s %(info.format_id)s")
	} else {
		base = append(base, "--no-progress")
	}
	if js {
		deno := filepath.Join(a.Cfg.Root, "tools/bin/deno")
		if fileExists(deno) {
			base = append(base, "--js-runtimes", "deno:"+deno)
		}
	} else {
		base = append(base, "--js-runtimes", "")
	}
	base = append(base, "--cache-dir", cache, "--socket-timeout", "15")
	var onLine func(string)
	if onProgress != nil {
		onLine = func(line string) {
			if m := progLineRe.FindStringSubmatch(line); m != nil {
				done, _ := strconv.ParseInt(m[1], 10, 64)
				total, _ := strconv.ParseInt(m[2], 10, 64)
				if total == 0 {
					total, _ = strconv.ParseInt(m[3], 10, 64)
				}
				onProgress(done, total, m[4])
			}
		}
	}
	env := append(os.Environ(), "PATH=/usr/local/bin:/usr/bin:/bin")
	return Exec(ctx, a.Cfg.YtDlp, append(base, args...), timeout, env, onLine)
}

// YtdlpErrorText — последняя строка stderr.
func YtdlpErrorText(r ExecResult) string {
	src := r.Stderr
	if src == "" {
		src = r.Stdout
	}
	var lines []string
	for _, l := range strings.Split(src, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) == 0 {
		if r.Stderr != "" {
			return r.Stderr
		}
		return "неизвестная ошибка"
	}
	return trimRunes(lines[len(lines)-1], 200)
}

// YtdlpList — записи площадки без скачивания: одна строка JSON на запись.
func (a *App) YtdlpList(ctx context.Context, u string, count int) ([]jx.M, error) {
	u, err := YtdlpCheckURL(u)
	if err != nil {
		return nil, err
	}
	r := a.YtdlpRun(ctx, []string{"--flat-playlist", "--playlist-end", strconv.Itoa(max(1, count)), "-j", "--", u}, ytdlpListTimeout, true, nil)
	if !r.OK && strings.TrimSpace(r.Stdout) == "" {
		return nil, Bad("Не удалось получить список: %s", YtdlpErrorText(r))
	}
	var items []jx.M
	for _, line := range strings.Split(r.Stdout, "\n") {
		if m := jx.Unmarshal([]byte(strings.TrimSpace(line))); m != nil {
			items = append(items, m)
		}
	}
	return items, nil
}

// ErrVideoTooLong — ролик длиннее лимита: повторять бессмысленно.
var ErrVideoTooLong = errors.New("ролик слишком длинный")

var watchURLRe = regexp.MustCompile(`^https://www\.youtube\.com/watch\?v=[A-Za-z0-9_-]{11}$`)

func YtdlpIsWatchURL(u string) bool { return watchURLRe.MatchString(u) }

// YtdlpDownload — скачать ролик в destDir; путь к файлу. Сколько скачиваний идёт сразу, решает очередь (videoWorkers).
func (a *App) YtdlpDownload(ctx context.Context, u, format, destDir string, maxBytes int64, onProgress func(done, total int64, format string)) (string, error) {
	u, err := YtdlpCheckURL(u)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(destDir, 0o775); err != nil {
		return "", Bad("Не удалось создать папку для ролика.")
	}
	watch := YtdlpIsWatchURL(u)
	args := []string{"-f", format, "--merge-output-format", "mp4", "--max-filesize", strconv.FormatInt(max(1, maxBytes), 10), "--no-playlist"}
	if watch {
		args = append(args, "--match-filter", fmt.Sprintf("duration<=%d", ytdlpWatchMaxSeconds))
	}
	tail := []string{"--no-simulate", "--print", "after_move:filepath", "-o", destDir + "/%(id)s.%(ext)s", "--", u}
	youtube := strings.Contains(u, "youtube.com/")
	run := func(args []string) ExecResult {
		r := a.YtdlpRun(ctx, append(args, tail...), ytdlpDownloadTimeout, !youtube, onProgress)
		if youtube && !r.OK {
			r = a.YtdlpRun(ctx, append(args, tail...), ytdlpDownloadTimeout, true, onProgress)
		}
		return r
	}
	r := run(args)
	var paths []string
	for _, l := range strings.Split(r.Stdout, "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "PROG ") {
			paths = append(paths, l)
		}
	}
	file := ""
	if len(paths) > 0 {
		file = paths[len(paths)-1]
	}
	if watch && r.OK && file == "" {
		// Длинный ролик целиком не берём, но превью играет: первая минута
		clip := []string{"-f", format, "--merge-output-format", "mp4", "--max-filesize", strconv.FormatInt(max(1, maxBytes), 10), "--no-playlist",
			"--download-sections", fmt.Sprintf("*0-%d", ytdlpWatchClipSeconds), "--force-keyframes-at-cuts"}
		r = run(clip)
		paths = nil
		for _, l := range strings.Split(r.Stdout, "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "PROG ") {
				paths = append(paths, l)
			}
		}
		if len(paths) > 0 {
			file = paths[len(paths)-1]
		}
	}
	if watch && r.OK && file == "" {
		return "", fmt.Errorf("%w: Ролик длиннее %d минут — откройте его на YouTube", ErrVideoTooLong, ytdlpWatchMaxSeconds/60)
	}
	absDest, _ := filepath.Abs(destDir)
	absFile, _ := filepath.Abs(file)
	if !r.OK || !fileExists(file) || !strings.HasPrefix(absFile, absDest+"/") {
		return "", Bad("Не удалось скачать ролик: %s", YtdlpErrorText(r))
	}
	return file, nil
}

func (a *App) YtdlpCodec(file string) string {
	r := Exec(context.Background(), a.Cfg.FFprobe, []string{"-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0", file}, 20*time.Second, nil, nil)
	if !r.OK {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(r.Stdout))
}

// YtdlpH264 — не H.264 — перекодировать и заменить исходный файл.
func (a *App) YtdlpH264(file string) (string, error) {
	if a.YtdlpCodec(file) == "h264" {
		return file, nil
	}
	out := file + ".h264.mp4"
	ctx := a.ctxOrBg()
	if !acquireFFmpeg(ctx) {
		return "", ctx.Err()
	}
	defer releaseFFmpeg()
	r := Exec(ctx, a.Cfg.FFmpeg, []string{"-v", "error", "-y", "-i", file, "-c:v", "libx264", "-preset", "veryfast", "-crf", "23",
		"-c:a", "aac", "-movflags", "+faststart", "-f", "mp4", out}, ytdlpConvertTimeout, nil, nil)
	if !r.OK || !fileExists(out) {
		os.Remove(out)
		return "", Bad("Не удалось перекодировать ролик в H.264: %s", YtdlpErrorText(r))
	}
	if err := os.Rename(out, file); err != nil {
		os.Remove(out)
		return "", Bad("Не удалось заменить ролик перекодированным.")
	}
	return file, nil
}

// YtdlpFormatFor — формат по площадке: шортсы — H.264 до 480p, обычный ролик — до 720p, TikTok — H.264 или MP4.
func YtdlpFormatFor(page string) string {
	if rutubeVideoRe.MatchString(page) {
		// Rutube advertises mirrored HLS formats. Prefer the first CDN and a
		// modest resolution; the fallback keeps older/single-CDN responses usable.
		return "b[height<=480][format_id$=-0]/b[height<=480]/b"
	}
	if YtdlpIsWatchURL(page) {
		h := ytdlpWatchMaxHeight
		return fmt.Sprintf("bv*[vcodec^=avc1][height<=%d]+ba[ext=m4a]/b[ext=mp4][height<=%d]/b[ext=mp4]", h, h)
	}
	if strings.Contains(page, "youtube.com/") {
		return fmt.Sprintf("bv*[vcodec^=avc1][height<=%d]+ba[ext=m4a]/b[ext=mp4]", ytdlpShortsMaxHeight)
	}
	return "b[vcodec^=h264]/b[vcodec^=avc1]/b[ext=mp4]/b"
}

// ---- Очередь роликов ----

type VideoJob struct {
	UID    string `json:"uid"`
	Key    string `json:"key"`
	Page   string `json:"page"`
	Via    string `json:"via"`
	Tries  int    `json:"tries"`
	NextAt int64  `json:"next_at"`
}

var tmePostRe = regexp.MustCompile(`^https://t\.me/`)

func jobVia(j VideoJob) string {
	if j.Via == "ytdlp" || j.Via == "tgu" {
		return j.Via
	}
	if tmePostRe.MatchString(j.Page) {
		return "tgu"
	}
	return "ytdlp"
}

func (a *App) videoQueue(ctx context.Context) []VideoJob {
	return KV(a, ctx, ytdlpQueueKey, []VideoJob{})
}

func (a *App) videoQueueUpdate(ctx context.Context, fn func([]VideoJob) []VideoJob) []VideoJob {
	out, _ := KVUpdate(a, ctx, ytdlpQueueKey, 0, func(jobs []VideoJob, _ bool) *[]VideoJob {
		next := fn(jobs)
		return &next
	})
	return out
}

// VideoEnqueue — запись пула в очередь на ролик (одна задача на запись). front — первым (ролик ждут).
func (a *App) VideoEnqueue(ctx context.Context, uid, key, page, via string, front bool) {
	via = jobVia(VideoJob{Via: via, Page: page})
	a.videoQueueUpdate(ctx, func(jobs []VideoJob) []VideoJob {
		for i, j := range jobs {
			if j.UID == uid && j.Key == key {
				if front {
					jobs = append(append([]VideoJob{j}, jobs[:i]...), jobs[i+1:]...)
				}
				return jobs
			}
		}
		nj := VideoJob{UID: uid, Key: key, Page: page, Via: via}
		if len(jobs) < ytdlpQueueMax {
			if front {
				jobs = append([]VideoJob{nj}, jobs...)
			} else {
				jobs = append(jobs, nj)
			}
		}
		return jobs
	})
	a.KickVideoWorker()
}

func videoIsPreviewKey(k string) bool { return strings.HasPrefix(k, "preview:") }

func hashKey(prefix, s string) string {
	h := sha256.Sum256([]byte(s))
	return prefix + hex.EncodeToString(h[:])[:16]
}

func VideoPreviewKey(u string) string      { return hashKey("preview:", u) }
func videoPreviewStateKey(u string) string { return hashKey("video_preview:", u) }

// ---- Прогресс скачивания (в памяти) ----

type videoProgress struct {
	Stage   string
	Percent *float64
	At      time.Time
}

var progress = struct {
	sync.Mutex
	m map[string]videoProgress
}{m: map[string]videoProgress{}}

var progressStages = map[string]string{"probe": "Получаю данные видео", "download": "Скачиваю", "merge": "Склеиваю", "convert": "Перекодирую", "telegram": "Скачиваю из Telegram"}

func progressKey(uid, key string) string { return uid + "|" + key }

func progressSave(j VideoJob, stage string, percent *float64) {
	progress.Lock()
	defer progress.Unlock()
	if percent != nil {
		p := float64(int(min(100, max(0, *percent))*10)) / 10
		percent = &p
	}
	progress.m[progressKey(j.UID, j.Key)] = videoProgress{stage, percent, time.Now()}
}

// VideoProgressPublic — {stage, label, percent, place}; nil — задачи нет.
func (a *App) VideoProgressPublic(ctx context.Context, uid, key string) jx.M {
	place := 0
	t := now()
	for _, j := range a.videoQueue(ctx) {
		if j.NextAt <= t {
			place++
		}
		if j.UID != uid || j.Key != key {
			continue
		}
		if wait := j.NextAt - t; wait > 0 {
			return jx.M{"stage": "retry", "label": fmt.Sprintf("Повтор через %d с", wait), "percent": nil, "place": 0}
		}
		progress.Lock()
		p, ok := progress.m[progressKey(uid, key)]
		progress.Unlock()
		if ok && time.Since(p.At) < 10*time.Minute {
			label := progressStages[p.Stage]
			if label == "" {
				label = "Скачиваю"
			}
			var pc any
			if p.Percent != nil {
				pc = *p.Percent
			}
			return jx.M{"stage": p.Stage, "label": label, "percent": pc, "place": 0}
		}
		label := "Сейчас начнётся"
		if place > 1 {
			label = fmt.Sprintf("В очереди: %d-й", place)
		}
		return jx.M{"stage": "queued", "label": label, "percent": nil, "place": place}
	}
	return nil
}

func progressTracker(j VideoJob) func(done, total int64, format string) {
	var formats []string
	return func(done, total int64, format string) {
		if !contains(formats, format) {
			formats = append(formats, format)
		}
		if total <= 0 {
			progressSave(j, "download", nil)
			return
		}
		part := min(1, float64(done)/float64(total))
		pc := 85 * part
		if len(formats) > 1 {
			pc = 85 + 12*part
		}
		stage := "download"
		if len(formats) > 1 && part >= 1 {
			stage = "merge"
		}
		progressSave(j, stage, &pc)
	}
}

// ---- Воркер очереди: горутина, будится при постановке задачи и по окончании ролика ----

var videoWake = make(chan struct{}, 1)

// Queue previews freely, but process only two yt-dlp videos at a time.
var videoWorkers = map[string]int{"ytdlp": 2, "tgu": 1}

// videoRunning — задачи, которые уже качаются (uid|key), и сколько их по способу.
var videoRunning = struct {
	sync.Mutex
	jobs map[string]bool
	via  map[string]int
}{jobs: map[string]bool{}, via: map[string]int{}}

func (a *App) KickVideoWorker() {
	select {
	case videoWake <- struct{}{}:
	default:
	}
}

// RunVideoWorker — обработка очереди роликов до остановки процесса: несколько роликов параллельно (videoWorkers).
func (a *App) RunVideoWorker(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	var wg sync.WaitGroup
	for {
		for ctx.Err() == nil {
			job := a.nextVideoJob(ctx)
			if job == nil {
				break
			}
			wg.Add(1)
			go func(j VideoJob) {
				defer wg.Done()
				defer a.videoJobRelease(j)
				a.runVideoJob(ctx, j)
			}(*job)
		}
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-videoWake:
		case <-t.C:
		}
	}
}

// nextVideoJob — следующая задача, которая ещё не качается и для которой есть свободный слот; задача сразу помечается занятой.
func (a *App) nextVideoJob(ctx context.Context) *VideoJob {
	t := now()
	var due []VideoJob
	videoRunning.Lock()
	defer videoRunning.Unlock()
	for _, j := range a.videoQueue(ctx) {
		if j.NextAt <= t && !videoRunning.jobs[progressKey(j.UID, j.Key)] && videoRunning.via[jobVia(j)] < videoWorkers[jobVia(j)] {
			due = append(due, j)
		}
	}
	sort.SliceStable(due, func(i, k int) bool {
		pi, pk := videoIsPreviewKey(due[i].Key), videoIsPreviewKey(due[k].Key)
		if pi != pk {
			return pi
		}
		return due[i].NextAt < due[k].NextAt
	})
	if len(due) == 0 {
		return nil
	}
	j := due[0]
	videoRunning.jobs[progressKey(j.UID, j.Key)] = true
	videoRunning.via[jobVia(j)]++
	return &j
}

// videoJobRelease — слот задачи свободен: воркер берёт следующую.
func (a *App) videoJobRelease(j VideoJob) {
	videoRunning.Lock()
	delete(videoRunning.jobs, progressKey(j.UID, j.Key))
	videoRunning.via[jobVia(j)]--
	videoRunning.Unlock()
	a.KickVideoWorker()
}

func (a *App) runVideoJob(ctx context.Context, j VideoJob) {
	u := a.User(ctx, j.UID, a.ParserLogger(j.UID))
	if videoIsPreviewKey(j.Key) {
		a.videoPreviewSave(ctx, j.Page, jx.M{"status": "running", "error": ""})
	}
	times := []string{}
	stage := func(name string, fn func() error) error {
		t0 := time.Now()
		err := fn()
		times = append(times, fmt.Sprintf("%s %.1f с", name, time.Since(t0).Seconds()))
		return err
	}
	tries := j.Tries + 1
	label := "записи пула "
	if videoIsPreviewKey(j.Key) {
		label = "превью "
	}
	label += trimRunes(j.Page, 80)
	var file, local string
	err := func() error {
		tgu := jobVia(j) == "tgu"
		if tgu {
			progressSave(j, "telegram", nil)
		} else {
			progressSave(j, "probe", nil)
		}
		// Своя папка у каждой задачи: один и тот же ролик могут качать сразу две задачи (превью и запись пула)
		dir := filepath.Join(a.ytdlpCacheDir(), "downloads", sha1Short(j.UID+"|"+j.Key, 16))
		defer os.RemoveAll(dir)
		if err := stage("скачивание", func() error {
			var err error
			if tgu {
				file, err = a.tguDownload(ctx, j, dir)
			} else {
				file, err = a.YtdlpDownload(ctx, j.Page, YtdlpFormatFor(j.Page), dir, config.TelegramUploadMaxBytes, progressTracker(j))
			}
			return err
		}); err != nil {
			return err
		}
		codec := a.YtdlpCodec(file)
		name := "подготовка"
		if codec != "h264" {
			pc := 98.0
			progressSave(j, "convert", &pc)
			name = "перекодирование"
		}
		if err := stage(name, func() error {
			h, err := a.YtdlpH264(file)
			if err != nil {
				return err
			}
			// Ролик yt-dlp уже H.264 до 720p с moov в начале — в кэш как есть: тяжёлое перекодирование только задержало бы показ
			if !tgu && Mp4HasFastStart(h) {
				local = u.MediaStoreVideo(j.Page, h, true, nil)
			} else {
				local = u.CacheDownloadedVideo(j.Page, h)
			}
			return nil
		}); err != nil {
			return err
		}
		if local == "" {
			return errors.New("не удалось сохранить ролик в кэш")
		}
		return nil
	}()
	if err != nil {
		u.Log("-> Ролик %s — попытка %d не удалась: %s.", label, tries, strings.Join(times, ", "))
		a.videoJobFailed(ctx, u, j, err.Error(), errors.Is(err, ErrVideoTooLong))
		return
	}
	a.videoAttach(ctx, j, local)
	a.videoDropJob(ctx, j)
	u.Log("-> Ролик %s готов с попытки %d: %s.", label, tries, strings.Join(times, ", "))
}

var tmePostPartsRe = regexp.MustCompile(`^https://t\.me/([A-Za-z0-9_]{4,32})/(\d+)$`)

func (a *App) tguDownload(ctx context.Context, j VideoJob, dir string) (string, error) {
	m := tmePostPartsRe.FindStringSubmatch(j.Page)
	if m == nil {
		return "", errors.New("не ссылка на пост Telegram")
	}
	id, _ := strconv.Atoi(m[2])
	return a.TelegramUserDownloadVideo(ctx, j.UID, m[1], id, dir)
}

// videoAttach — ролик готов: запись пула получает video и video_local; превью — только состояние.
func (a *App) videoAttach(ctx context.Context, j VideoJob, local string) {
	if videoIsPreviewKey(j.Key) {
		a.videoPreviewSave(ctx, j.Page, jx.M{"status": "done", "local": local, "error": "", "at": now()})
		return
	}
	u := a.User(ctx, j.UID, nil)
	u.PoolUpdate(func(pool []jx.M) []jx.M {
		for _, it := range pool {
			if ContentImageKey(it) == j.Key && jx.Str(it, "video_via") == jobVia(j) {
				it["video"], it["video_local"] = local, local
			}
		}
		return pool
	})
}

func (a *App) videoDropJob(ctx context.Context, j VideoJob) {
	progress.Lock()
	delete(progress.m, progressKey(j.UID, j.Key))
	progress.Unlock()
	a.videoQueueUpdate(ctx, func(jobs []VideoJob) []VideoJob {
		out := []VideoJob{}
		for _, x := range jobs {
			if !(x.UID == j.UID && x.Key == j.Key) {
				out = append(out, x)
			}
		}
		return out
	})
}

// videoJobFailed — повтор через минуту, потом ещё; после трёх — запись помечается недоступным видео.
func (a *App) videoJobFailed(ctx context.Context, u *U, j VideoJob, reason string, final bool) {
	tries := j.Tries + 1
	if final {
		tries = ytdlpTries
	}
	u.Log("-> Ролик не скачан (%d из %d): %s", tries, ytdlpTries, reason)
	preview := videoIsPreviewKey(j.Key)
	if preview && tries >= ytdlpTries {
		a.videoPreviewSave(ctx, j.Page, jx.M{"status": "failed", "error": reason})
		a.videoDropJob(ctx, j)
		return
	}
	if tries < ytdlpTries {
		if preview {
			a.videoPreviewSave(ctx, j.Page, jx.M{"status": "queued", "error": reason})
		}
		a.videoQueueUpdate(ctx, func(jobs []VideoJob) []VideoJob {
			for i := range jobs {
				if jobs[i].UID == j.UID && jobs[i].Key == j.Key {
					jobs[i].Tries, jobs[i].NextAt = tries, now()+int64(60*tries)
				}
			}
			return jobs
		})
		return
	}
	u.PoolUpdate(func(pool []jx.M) []jx.M {
		for _, it := range pool {
			if ContentImageKey(it) == j.Key {
				it["video_unavailable"] = true
			}
		}
		return pool
	})
	a.videoDropJob(ctx, j)
}

// ---- Ролик по кнопке и первые ролики превью ----

var videoPreviewLimits = map[string]int{"tgu": 2, "ytdlp": 12}

const videoPreviewFirst = 12

var (
	tmePostURLRe  = regexp.MustCompile(`^https://t\.me/[A-Za-z0-9_]{4,32}/\d+$`)
	tiktokVideoRe = regexp.MustCompile(`^https://www\.tiktok\.com/@([^/]+)/video/\d{5,25}$`)
	ytShortsURLRe = regexp.MustCompile(`^https://www\.youtube\.com/shorts/[A-Za-z0-9_-]{11}$`)
	rutubeVideoRe = regexp.MustCompile(`^https://(?:www\.)?rutube\.ru/video/[a-fA-F0-9]{32}/?$`)
	vkVideoRe     = regexp.MustCompile(`^https://(?:www\.)?(?:vk\.com|vkvideo\.ru)/video-?[0-9]+_[0-9]+$`)
)

// VideoPageVia — поддерживаемая публичная страница видео или "" для неизвестных адресов.
func VideoPageVia(u string) string {
	if tmePostURLRe.MatchString(u) {
		return "tgu"
	}
	if m := tiktokVideoRe.FindStringSubmatch(u); m != nil && IsValidTikTokUser(m[1]) {
		return "ytdlp"
	}
	if ytShortsURLRe.MatchString(u) || YtdlpIsWatchURL(u) || rutubeVideoRe.MatchString(u) || vkVideoRe.MatchString(u) {
		return "ytdlp"
	}
	return ""
}

// TelegramBigVideosViaAccount — большие видео Telegram без файла докачивает личный аккаунт (video_via = tgu).
func (u *U) TelegramBigVideosViaAccount(posts []jx.M) []jx.M {
	if !u.TelegramUserConnected() {
		return posts
	}
	for _, p := range posts {
		if jx.Bool(p, "video_unavailable") && VideoPageVia(jx.Str(p, "url")) == "tgu" {
			p["video_unavailable"], p["video_via"], p["video_page"] = false, "tgu", jx.Str(p, "url")
		}
	}
	return posts
}

// VideoPreviewStatus — {status: none|queued|running|done|failed, local, error, at}.
func (a *App) VideoPreviewStatus(ctx context.Context, u string) jx.M {
	st := jx.M{"status": "none", "local": "", "error": "", "at": 0}
	var saved jx.M
	if a.KVGet(ctx, videoPreviewStateKey(u), &saved) {
		for k := range st {
			if v, ok := saved[k]; ok {
				st[k] = v
			}
		}
	}
	if jx.Str(st, "status") == "done" && a.MediaExisting(jx.Str(st, "local")) == "" {
		return jx.M{"status": "none", "local": "", "error": "", "at": 0}
	}
	return st
}

func (a *App) videoPreviewSave(ctx context.Context, u string, fields jx.M) {
	KVUpdate(a, ctx, videoPreviewStateKey(u), 2*24*time.Hour, func(old jx.M, _ bool) *jx.M {
		if old == nil {
			old = jx.M{}
		}
		for k, v := range fields {
			old[k] = v
		}
		return &old
	})
}

func (a *App) videoPreviewJobsOf(ctx context.Context, uid, via string) int {
	n := 0
	for _, j := range a.videoQueue(ctx) {
		if j.UID == uid && videoIsPreviewKey(j.Key) && jobVia(j) == via {
			n++
		}
	}
	return n
}

func (a *App) videoPreviewJobExists(ctx context.Context, u, uid string) bool {
	key := VideoPreviewKey(u)
	for _, j := range a.videoQueue(ctx) {
		if j.UID == uid && j.Key == key {
			return true
		}
	}
	return false
}

// VideoFetchEnqueue — ролик адреса в очередь: одна задача на адрес, лимит на пользователя. limited — лимит исчерпан.
func (a *App) VideoFetchEnqueue(ctx context.Context, u, via, uid string, front bool) (jx.M, bool) {
	st := a.VideoPreviewStatus(ctx, u)
	status := jx.Str(st, "status")
	if status == "done" {
		return st, false
	}
	if (status == "queued" || status == "running") && a.videoPreviewJobExists(ctx, u, uid) {
		if front && status == "queued" {
			a.VideoEnqueue(ctx, uid, VideoPreviewKey(u), u, via, true)
		}
		return st, false
	}
	if a.videoPreviewJobsOf(ctx, uid, via) >= videoPreviewLimits[via] && !(front && a.videoPreviewEvictOne(ctx, uid, via)) {
		return st, true
	}
	a.videoPreviewSave(ctx, u, jx.M{"status": "queued", "local": "", "error": "", "at": now()})
	a.VideoEnqueue(ctx, uid, VideoPreviewKey(u), u, via, front)
	return a.VideoPreviewStatus(ctx, u), false
}

func (a *App) videoPreviewEvictOne(ctx context.Context, uid, via string) bool {
	var evicted *VideoJob
	a.videoQueueUpdate(ctx, func(jobs []VideoJob) []VideoJob {
		for i := len(jobs) - 1; i >= 0; i-- {
			j := jobs[i]
			if j.UID != uid || !videoIsPreviewKey(j.Key) || jobVia(j) != via || jx.Str(a.VideoPreviewStatus(ctx, j.Page), "status") != "queued" {
				continue
			}
			evicted = &j
			return append(jobs[:i], jobs[i+1:]...)
		}
		return jobs
	})
	if evicted == nil {
		return false
	}
	a.videoPreviewSave(ctx, evicted.Page, jx.M{"status": "none", "local": "", "error": "", "at": now()})
	return true
}

// VideoPreviewApply — посты с роликом: скачанный — с адресом кэша, ждущий — с пометкой video_pending.
func (a *App) VideoPreviewApply(ctx context.Context, posts []jx.M) []jx.M {
	for _, p := range posts {
		page := jx.Str(p, "video_page")
		if jx.Str(p, "video") != "" || page == "" || VideoPageVia(page) == "" {
			continue
		}
		st := a.VideoPreviewStatus(ctx, page)
		switch jx.Str(st, "status") {
		case "done":
			p["video"], p["video_local"], p["video_pending"] = st["local"], st["local"], false
		case "queued", "running":
			p["video_pending"] = true
		default:
			p["video_pending"] = false
		}
	}
	return posts
}

// VideoPreviewPrepare — первые ролики превью кандидата ставятся в очередь сами, впереди роликов прежних превью:
// с конца и каждый в начало очереди, чтобы первый пост качался первым; лимит вытесняет ещё не начатые ролики прежних превью.
func (a *App) VideoPreviewPrepare(ctx context.Context, posts []jx.M, uid string) []jx.M {
	type want struct{ page, via string }
	var wants []want
	for _, p := range posts {
		if len(wants) >= videoPreviewFirst {
			break
		}
		page := jx.Str(p, "video_page")
		via := VideoPageVia(page)
		if jx.Str(p, "video") != "" || page == "" || via == "" || jx.Str(a.VideoPreviewStatus(ctx, page), "status") == "done" {
			continue
		}
		wants = append(wants, want{page, via})
	}
	for i := len(wants) - 1; i >= 0; i-- {
		a.VideoFetchEnqueue(ctx, wants[i].page, wants[i].via, uid, true)
	}
	return a.VideoPreviewApply(ctx, posts)
}

// VideoPreviewKeepNames — файлы, скачанные по кнопке за сутки: очистка кэша их не трогает.
func (a *App) VideoPreviewKeepNames() map[string]bool {
	out := map[string]bool{}
	rows, err := a.DB.Pool.Query(a.ctxOrBg(), `SELECT value FROM kv WHERE key LIKE 'video_preview:%'`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var st struct {
			Status string `json:"status"`
			Local  string `json:"local"`
			At     int64  `json:"at"`
		}
		if json.Unmarshal(raw, &st) != nil || st.Status != "done" || now()-st.At >= config.DaySeconds {
			continue
		}
		if p := a.MediaPath(st.Local); p != "" {
			out[filepath.Base(p)] = true
		}
	}
	return out
}

// PoolItemsEnqueueVideos — записи пула с ещё не скачанным роликом — в очередь (обычные ролики YouTube — только по кнопке).
func (a *App) PoolItemsEnqueueVideos(ctx context.Context, items []jx.M, uid string) {
	for _, it := range items {
		via := jx.Str(it, "video_via")
		page := jx.Str(it, "video_page")
		if (via != "ytdlp" && via != "tgu") || jx.Str(it, "video") != "" || page == "" || YtdlpIsWatchURL(page) {
			continue
		}
		a.VideoEnqueue(ctx, uid, ContentImageKey(it), page, via, false)
	}
}

func sha1Short(s string, n int) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:n]
}
