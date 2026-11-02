package tgclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/auth/qrlogin"
	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/styling"
	"github.com/gotd/td/telegram/query"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"rsc.io/qr"
)

var authErrPasswordNeeded = auth.ErrPasswordAuthNeeded

func authSendCodeOptions() auth.SendCodeOptions { return auth.SendCodeOptions{} }

type dialogsElem = dialogs.Elem

func queryDialogs(api *tg.Client) *dialogs.GetDialogsQueryBuilder { return query.GetDialogs(api) }

// ---- Видео поста канала ----

func videoDocument(m *tg.Message) *tg.Document {
	md, ok := m.Media.(*tg.MessageMediaDocument)
	if !ok {
		return nil
	}
	dc, ok := md.GetDocument()
	if !ok {
		return nil
	}
	d, ok := dc.(*tg.Document)
	if !ok {
		return nil
	}
	if strings.HasPrefix(d.MimeType, "video/") || d.MimeType == "image/gif" {
		return d
	}
	for _, a := range d.Attributes {
		switch a.(type) {
		case *tg.DocumentAttributeVideo, *tg.DocumentAttributeAnimated:
			return d
		}
	}
	return nil
}

// DownloadVideo — видео поста публичного канала (в веб-версии t.me большие не отдаются) в каталог destDir.
func DownloadVideo(ctx context.Context, acc *Account, channel string, postID int, destDir string, maxBytes int64) (string, int64, error) {
	var path string
	var size int64
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		ch, err := resolveChannel(ctx, api, channel)
		if err != nil {
			return err
		}
		res, err := api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{Channel: inputChannel(ch), ID: []tg.InputMessageClass{&tg.InputMessageID{ID: postID}}})
		if err != nil {
			return err
		}
		m, _ := res.AsModified()
		var doc *tg.Document
		if m != nil {
			for _, mc := range m.GetMessages() {
				if msg, ok := mc.(*tg.Message); ok && msg.ID == postID {
					doc = videoDocument(msg)
				}
			}
		}
		if doc == nil {
			return fail("В посте нет видео.")
		}
		if doc.Size > maxBytes {
			return fail("Видео слишком большое: %d МБ.", doc.Size>>20)
		}
		ext := ".mp4"
		if doc.MimeType == "image/gif" {
			ext = ".gif"
		} else if doc.MimeType == "video/webm" {
			ext = ".webm"
		}
		path = filepath.Join(destDir, fmt.Sprintf("tg-%s-%d%s", ch.Username, postID, ext))
		loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
		if _, err := downloader.NewDownloader().Download(api, loc).ToPath(ctx, path); err != nil {
			os.Remove(path)
			return err
		}
		size = doc.Size
		return nil
	})
	return path, size, err
}

// ---- Отложенные посты ----

// Media — файл поста: photo | video | animation.
type Media struct {
	Kind     string
	Path     string
	Width    int
	Height   int
	Duration int
}

type uploadProgress struct {
	total    int64
	done     map[int64]int64
	base     int64
	last     time.Time
	progress func(float64)
}

func (p *uploadProgress) Chunk(_ context.Context, s uploader.ProgressState) error {
	p.done[s.ID] = s.Uploaded
	if p.progress == nil || p.total == 0 || time.Since(p.last) < 300*time.Millisecond {
		return nil
	}
	p.last = time.Now()
	var sum int64
	for _, v := range p.done {
		sum += v
	}
	p.progress(float64(sum) / float64(p.total) * 100)
	return nil
}

// scheduledIDs — номера отложенных сообщений из ответа (по ним их потом отменяют).
func scheduledIDs(u tg.UpdatesClass) []int {
	var ids []int
	var list []tg.UpdateClass
	switch v := u.(type) {
	case *tg.Updates:
		list = v.Updates
	case *tg.UpdatesCombined:
		list = v.Updates
	case *tg.UpdateShort:
		list = []tg.UpdateClass{v.Update}
	}
	for _, x := range list {
		if n, ok := x.(*tg.UpdateNewScheduledMessage); ok {
			ids = append(ids, n.Message.GetID())
		}
	}
	return ids
}

// Schedule — пост в «Отложенные» канала: уйдёт в назначенное время, даже если сервер выключен.
func Schedule(ctx context.Context, acc *Account, ref, text string, media []Media, at time.Time, progress func(float64)) ([]int, error) {
	var ids []int
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		ch, err := findChannel(ctx, api, ref)
		if err != nil {
			return err
		}
		b := message.NewSender(api).To(inputPeer(ch)).Schedule(at)
		var res tg.UpdatesClass
		if len(media) == 0 {
			res, err = b.NoWebpage().Text(ctx, text)
		} else {
			p := &uploadProgress{done: map[int64]int64{}, progress: progress}
			for _, m := range media {
				if st, err := os.Stat(m.Path); err == nil {
					p.total += st.Size()
				}
			}
			up := uploader.NewUploader(api).WithProgress(p)
			var opts []message.MultiMediaOption
			for i, m := range media {
				f, err := up.FromPath(ctx, m.Path)
				if err != nil {
					return err
				}
				var caption []styling.StyledTextOption
				if i == 0 && text != "" {
					caption = []styling.StyledTextOption{styling.Plain(text)}
				}
				switch m.Kind {
				case "video":
					opts = append(opts, message.UploadedDocument(f, caption...).MIME("video/mp4").Filename(filepath.Base(m.Path)).
						Video().SupportsStreaming().Resolution(m.Width, m.Height).DurationSeconds(m.Duration))
				case "animation":
					opts = append(opts, message.UploadedDocument(f, caption...).MIME("image/gif").Filename(filepath.Base(m.Path)))
				default:
					opts = append(opts, message.UploadedPhoto(f, caption...))
				}
			}
			if len(opts) == 1 {
				res, err = b.Media(ctx, opts[0])
			} else {
				res, err = b.Album(ctx, opts[0], opts[1:]...)
			}
		}
		if err != nil {
			return err
		}
		ids = scheduledIDs(res)
		return nil
	})
	return ids, err
}

// ---- Вход по QR-коду ----

// QRState — состояние входа: waiting (url, svg) | needs_password | ok | expired | error.
type QRState struct {
	Status  string `json:"status"`
	URL     string `json:"url,omitempty"`
	SVG     string `json:"svg,omitempty"`
	Session string `json:"-"`
	User    *User  `json:"user,omitempty"`
	Error   string `json:"error,omitempty"`
	At      int64  `json:"at"`
}

// QRSVG — QR-код ссылки в SVG (тёмные модули на белом).
func QRSVG(url string) string {
	code, err := qr.Encode(url, qr.M)
	if err != nil {
		return ""
	}
	const scale, border = 6, 2
	n := code.Size
	side := (n + 2*border) * scale
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d"><rect width="100%%" height="100%%" fill="#fff"/><path fill="#000" d="`, side, side, side, side)
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			if code.Black(x, y) {
				fmt.Fprintf(&b, "M%d %dh%dv%dh-%dz", (x+border)*scale, (y+border)*scale, scale, scale, scale)
			}
		}
	}
	b.WriteString(`"/></svg>`)
	return b.String()
}

// QRLogin — вход по QR (Настройки → Устройства → Подключить устройство). on вызывается на каждое изменение состояния;
// работает до lifetime, ctx отменяет вход (например, запущен новый).
func QRLogin(ctx context.Context, appID int, appHash string, lifetime time.Duration, on func(QRState)) {
	ctx, cancel := context.WithTimeout(ctx, lifetime)
	defer cancel()
	acc := &Account{AppID: appID, AppHash: appHash}
	d := tg.NewUpdateDispatcher()
	loggedIn := qrlogin.OnLoginToken(d)
	var me *User
	err := Run(ctx, acc, d, func(ctx context.Context, c *telegram.Client) error {
		_, err := c.QR().Auth(ctx, loggedIn, func(ctx context.Context, t qrlogin.Token) error {
			on(QRState{Status: "waiting", URL: t.URL(), SVG: QRSVG(t.URL()), At: time.Now().Unix()})
			return nil
		})
		if tgerr.Is(err, "SESSION_PASSWORD_NEEDED") || errors.Is(err, auth.ErrPasswordAuthNeeded) {
			return ErrNeedsPassword
		}
		if err != nil {
			return err
		}
		u, err := c.Self(ctx)
		if err == nil {
			me = userOf(u)
		}
		return err
	})
	switch {
	case errors.Is(err, ErrNeedsPassword):
		on(QRState{Status: "needs_password", Session: acc.Session, At: time.Now().Unix()})
	case errors.Is(err, context.DeadlineExceeded) || (err != nil && err.Error() == "Telegram не ответил вовремя."):
		on(QRState{Status: "expired", At: time.Now().Unix()})
	case errors.Is(err, context.Canceled):
	case err != nil:
		on(QRState{Status: "error", Error: err.Error(), At: time.Now().Unix()})
	default:
		on(QRState{Status: "ok", Session: acc.Session, User: me, At: time.Now().Unix()})
	}
}
