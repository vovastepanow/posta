// Package tgclient — личный аккаунт Telegram (MTProto, gotd/td) вместо прежнего tools/tg_user.py (Telethon).
// Каждый вызов — отдельное соединение с сессией аккаунта; сессия после вызова возвращается в Account.Session.
// Сессии Telethon (StringSession «1…») читаются и при первом же вызове сохраняются в формате gotd.
package tgclient

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Account — ключи приложения и сессия аккаунта (секреты: не логировать).
type Account struct {
	AppID   int
	AppHash string
	Session string
}

type memStorage struct {
	mu   sync.Mutex
	data []byte
}

func (m *memStorage) LoadSession(context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.data) == 0 {
		return nil, session.ErrNotFound
	}
	return append([]byte(nil), m.data...), nil
}

func (m *memStorage) StoreSession(_ context.Context, data []byte) error {
	m.mu.Lock()
	m.data = append([]byte(nil), data...)
	m.mu.Unlock()
	return nil
}

func storageFor(ctx context.Context, s string) (*memStorage, error) {
	st := &memStorage{}
	s = strings.TrimSpace(s)
	switch {
	case s == "":
	case strings.HasPrefix(s, "{"):
		st.data = []byte(s)
	default:
		data, err := session.TelethonSession(s)
		if err != nil {
			return nil, fmt.Errorf("сессия Telegram повреждена: подключите аккаунт заново")
		}
		if err := (&session.Loader{Storage: st}).Save(ctx, data); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// options — клиент представляется как «Posta» (так он виден в «Устройствах» Telegram).
func options(st session.Storage, h telegram.UpdateHandler) telegram.Options {
	return telegram.Options{SessionStorage: st, UpdateHandler: h, RetryInterval: 2 * time.Second, MaxRetries: 2, DialTimeout: 20 * time.Second,
		Device: telegram.DeviceConfig{DeviceModel: "Posta", SystemVersion: "Linux", AppVersion: "1.0", SystemLangCode: "ru", LangCode: "ru"}}
}

// Run — соединение от имени аккаунта; обновлённая сессия — в acc.Session.
func Run(ctx context.Context, acc *Account, h telegram.UpdateHandler, fn func(ctx context.Context, c *telegram.Client) error) error {
	st, err := storageFor(ctx, acc.Session)
	if err != nil {
		return err
	}
	c := telegram.NewClient(acc.AppID, acc.AppHash, options(st, h))
	err = c.Run(ctx, func(ctx context.Context) error { return fn(ctx, c) })
	if data, e := st.LoadSession(ctx); e == nil && len(data) > 0 {
		acc.Session = string(data)
	}
	return Describe(err)
}

// RunAuthorized — как Run, но сначала проверяет, что вход выполнен.
func RunAuthorized(ctx context.Context, acc *Account, fn func(ctx context.Context, api *tg.Client) error) error {
	return Run(ctx, acc, nil, func(ctx context.Context, c *telegram.Client) error {
		st, err := c.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !st.Authorized {
			return ErrUnauthorized
		}
		return fn(ctx, c.API())
	})
}

var ErrUnauthorized = errors.New("Вход устарел: подключите аккаунт заново.")

// ErrNeedsPassword — нужен пароль двухэтапной защиты.
var ErrNeedsPassword = errors.New("Нужен пароль двухэтапной защиты.")

var errorTexts = map[string]string{
	"API_ID_INVALID":         "api_id или api_hash не подходят: проверьте их на my.telegram.org.",
	"API_ID_PUBLISHED_FLOOD": "api_id или api_hash не подходят: проверьте их на my.telegram.org.",
	"PHONE_NUMBER_INVALID":   "Номер телефона указан неверно: нужен формат +79991234567.",
	"PHONE_NUMBER_BANNED":    "Этот номер заблокирован в Telegram.",
	"PHONE_CODE_INVALID":     "Код неверный.",
	"PHONE_CODE_EXPIRED":     "Код устарел: запросите новый.",
	"PASSWORD_HASH_INVALID":  "Пароль двухэтапной защиты неверный.",
	"SESSION_PASSWORD_NEEDED": "Нужен пароль двухэтапной защиты.",
	"AUTH_KEY_UNREGISTERED":  "Вход устарел: подключите аккаунт заново.",
	"SESSION_REVOKED":        "Сессия завершена в Telegram: подключите аккаунт заново.",
	"USER_DEACTIVATED":       "Аккаунт удалён или заблокирован.",
	"USER_DEACTIVATED_BAN":   "Аккаунт удалён или заблокирован.",
	"SCHEDULE_TOO_MUCH":      "В канале уже слишком много отложенных постов (лимит Telegram — 100).",
	"CHAT_ADMIN_REQUIRED":    "У вашего аккаунта нет права публиковать в этом канале.",
	"CHAT_WRITE_FORBIDDEN":   "У вашего аккаунта нет права публиковать в этом канале.",
	"CHANNEL_PRIVATE":        "Канал закрыт для этого аккаунта.",
	"USERNAME_NOT_OCCUPIED":  "Такого канала нет.",
	"USERNAME_INVALID":       "Такого канала нет.",
}

// Describe — ошибка Telegram понятным текстом.
func Describe(err error) error {
	if err == nil || errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNeedsPassword) || errors.Is(err, context.Canceled) {
		return err
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.New("Telegram не ответил вовремя.")
	}
	if d, ok := tgerr.AsFloodWait(err); ok {
		return &FloodError{Seconds: int(d.Seconds())}
	}
	if e, ok := tgerr.As(err); ok {
		if t, ok := errorTexts[e.Type]; ok {
			return errors.New(t)
		}
		return fmt.Errorf("Telegram: %s", e.Type)
	}
	var known *textError
	if errors.As(err, &known) {
		return known
	}
	return fmt.Errorf("Telegram: %v", err)
}

type textError struct{ msg string }

func (e *textError) Error() string { return e.msg }

func fail(format string, a ...any) error { return &textError{fmt.Sprintf(format, a...)} }

// FloodError — Telegram просит подождать.
type FloodError struct{ Seconds int }

func (e *FloodError) Error() string { return fmt.Sprintf("Telegram просит подождать %d с и повторить.", e.Seconds) }

// ---- Вход ----

type CodeInfo struct {
	PhoneCodeHash string `json:"phone_code_hash"`
	CodeType      string `json:"code_type"`
	NextType      string `json:"next_type"`
}

func codeTypeName(v any) string {
	if v == nil {
		return ""
	}
	n := strings.ToLower(fmt.Sprintf("%T", v))
	n = n[strings.LastIndex(n, ".")+1:]
	n = strings.TrimPrefix(n, "authsentcodetype")
	n = strings.TrimPrefix(n, "authcodetype")
	return n
}

func codeInfo(sent tg.AuthSentCodeClass) (CodeInfo, error) {
	s, ok := sent.(*tg.AuthSentCode)
	if !ok {
		return CodeInfo{}, fail("Telegram не отправил код: попробуйте вход по QR-коду.")
	}
	info := CodeInfo{PhoneCodeHash: s.PhoneCodeHash, CodeType: codeTypeName(s.Type)}
	if nt, ok := s.GetNextType(); ok {
		info.NextType = codeTypeName(nt)
	}
	return info, nil
}

func SendCode(ctx context.Context, acc *Account, phone string) (CodeInfo, error) {
	var info CodeInfo
	err := Run(ctx, acc, nil, func(ctx context.Context, c *telegram.Client) error {
		sent, err := c.Auth().SendCode(ctx, phone, authSendCodeOptions())
		if err != nil {
			return err
		}
		info, err = codeInfo(sent)
		return err
	})
	return info, err
}

// ResendCode — следующий способ доставки, который предлагает Telegram (обычно SMS после кода в приложении).
func ResendCode(ctx context.Context, acc *Account, phone, hash string) (CodeInfo, error) {
	var info CodeInfo
	err := Run(ctx, acc, nil, func(ctx context.Context, c *telegram.Client) error {
		sent, err := c.API().AuthResendCode(ctx, &tg.AuthResendCodeRequest{PhoneNumber: phone, PhoneCodeHash: hash})
		if err != nil {
			return err
		}
		info, err = codeInfo(sent)
		return err
	})
	return info, err
}

type User struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

func userOf(u *tg.User) *User {
	return &User{ID: u.ID, Name: strings.TrimSpace(u.FirstName + " " + u.LastName), Username: u.Username}
}

// SignIn — вход по коду; ErrNeedsPassword — нужен пароль (и он не передан).
func SignIn(ctx context.Context, acc *Account, phone, code, hash, password string) (*User, error) {
	var me *User
	err := Run(ctx, acc, nil, func(ctx context.Context, c *telegram.Client) error {
		_, err := c.Auth().SignIn(ctx, phone, code, hash)
		if errors.Is(err, authErrPasswordNeeded) {
			if password == "" {
				return ErrNeedsPassword
			}
			_, err = c.Auth().Password(ctx, password)
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
	return me, err
}

func Password(ctx context.Context, acc *Account, password string) (*User, error) {
	var me *User
	err := Run(ctx, acc, nil, func(ctx context.Context, c *telegram.Client) error {
		if _, err := c.Auth().Password(ctx, password); err != nil {
			return err
		}
		u, err := c.Self(ctx)
		if err == nil {
			me = userOf(u)
		}
		return err
	})
	return me, err
}

// ---- Каналы и группы ----

// Channel — публичный канал (не группа) с @именем.
type Channel struct {
	Username    string `json:"username"`
	Title       string `json:"title"`
	Subscribers int64  `json:"subscribers"`
	Origin      string `json:"origin"`
}

// Group — публичная группа-чат (мегагруппа) с @именем.
type Group struct {
	Username string `json:"username"`
	Title    string `json:"title"`
	Members  int64  `json:"members"`
	Query    string `json:"query"`
}

func channelInfo(ch tg.ChatClass, origin string) *Channel {
	c, ok := ch.(*tg.Channel)
	if !ok || !c.Broadcast || c.Username == "" {
		return nil
	}
	n, _ := c.GetParticipantsCount()
	return &Channel{Username: c.Username, Title: c.Title, Subscribers: int64(n), Origin: origin}
}

func groupInfo(ch tg.ChatClass, query string) *Group {
	c, ok := ch.(*tg.Channel)
	if !ok || c.Username == "" || c.Broadcast || !(c.Megagroup || c.Gigagroup) {
		return nil
	}
	n, _ := c.GetParticipantsCount()
	return &Group{Username: c.Username, Title: c.Title, Members: int64(n), Query: query}
}

func searchChats(ctx context.Context, api *tg.Client, queries []string, limit, maxQueries int, each func(ch tg.ChatClass, q string)) error {
	for i, q := range queries {
		if i >= maxQueries {
			break
		}
		q = strings.TrimSpace(q)
		if len([]rune(q)) < 2 {
			continue
		}
		res, err := api.ContactsSearch(ctx, &tg.ContactsSearchRequest{Q: q, Limit: min(limit, 100)})
		if err != nil {
			if _, flood := tgerr.AsFloodWait(err); flood {
				break // лимит поиска исчерпан: возвращаем, что успели найти
			}
			return err
		}
		for _, ch := range res.Chats {
			each(ch, q)
		}
	}
	return nil
}

// SearchChannels — глобальный поиск: реально существующие каналы в порядке, в каком их ранжирует Telegram.
func SearchChannels(ctx context.Context, acc *Account, queries []string, limit int) ([]Channel, error) {
	var out []Channel
	seen := map[string]bool{}
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		return searchChats(ctx, api, queries, limit, 12, func(ch tg.ChatClass, q string) {
			if c := channelInfo(ch, q); c != nil && !seen[strings.ToLower(c.Username)] {
				seen[strings.ToLower(c.Username)] = true
				out = append(out, *c)
			}
		})
	})
	return out, err
}

// SearchGroups — публичные группы (не каналы) по запросам, без вступления (радар лидов).
func SearchGroups(ctx context.Context, acc *Account, queries []string, limit int) ([]Group, error) {
	var out []Group
	seen := map[string]bool{}
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		return searchChats(ctx, api, queries, limit, 15, func(ch tg.ChatClass, q string) {
			if g := groupInfo(ch, q); g != nil && !seen[strings.ToLower(g.Username)] {
				seen[strings.ToLower(g.Username)] = true
				out = append(out, *g)
			}
		})
	})
	return out, err
}

func resolveChannel(ctx context.Context, api *tg.Client, username string) (*tg.Channel, error) {
	res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: strings.TrimPrefix(username, "@")})
	if err != nil {
		return nil, err
	}
	for _, ch := range res.Chats {
		if c, ok := ch.(*tg.Channel); ok {
			return c, nil
		}
	}
	return nil, fail("Это не канал и не группа.")
}

// findChannel — канал по @имени или числовому id (-100…); по id ищем среди диалогов аккаунта (иначе нет access_hash).
func findChannel(ctx context.Context, api *tg.Client, ref string) (*tg.Channel, error) {
	ref = strings.TrimSpace(ref)
	id, err := strconv.ParseInt(strings.TrimPrefix(ref, "-100"), 10, 64)
	if err != nil || strings.HasPrefix(ref, "@") {
		return resolveChannel(ctx, api, ref)
	}
	if id < 0 {
		id = -id
	}
	var found *tg.Channel
	errStop := errors.New("найден")
	err = queryDialogs(api).BatchSize(100).ForEach(ctx, func(ctx context.Context, e dialogsElem) error {
		if c, ok := e.Entities.Channel(id); ok {
			found = c
			return errStop
		}
		return nil
	})
	if found != nil {
		return found, nil
	}
	if err != nil && !errors.Is(err, errStop) {
		return nil, err
	}
	return nil, fail("Канал не найден среди чатов аккаунта: аккаунт должен состоять в канале.")
}

func inputChannel(c *tg.Channel) *tg.InputChannel {
	return &tg.InputChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
}

func inputPeer(c *tg.Channel) *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: c.ID, AccessHash: c.AccessHash}
}

// SimilarChannels — «похожие каналы» из рекомендаций Telegram для каждого канала из списка.
func SimilarChannels(ctx context.Context, acc *Account, refs []string) ([]Channel, error) {
	var out []Channel
	seen := map[string]bool{}
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		for i, ref := range refs {
			if i >= 8 {
				break
			}
			ref = strings.TrimPrefix(ref, "@")
			c, err := resolveChannel(ctx, api, ref)
			if err != nil {
				if _, flood := tgerr.AsFloodWait(err); flood {
					break
				}
				continue
			}
			res, err := api.ChannelsGetChannelRecommendations(ctx, &tg.ChannelsGetChannelRecommendationsRequest{Channel: inputChannel(c)})
			if err != nil {
				if _, flood := tgerr.AsFloodWait(err); flood {
					break
				}
				continue
			}
			for _, ch := range res.GetChats() {
				if info := channelInfo(ch, ref); info != nil && !seen[strings.ToLower(info.Username)] {
					seen[strings.ToLower(info.Username)] = true
					out = append(out, *info)
				}
			}
		}
		return nil
	})
	return out, err
}

// Resolved — проверка имени: Exists nil — не удалось проверить (сеть, лимит).
type Resolved struct {
	Username string `json:"username"`
	Exists   *bool  `json:"exists"`
	Title    string `json:"title,omitempty"`
	Error    string `json:"error,omitempty"`
}

// ResolveUsernames — существует ли публичный канал или группа с таким именем (не больше 10 за вызов).
func ResolveUsernames(ctx context.Context, acc *Account, names []string) ([]Resolved, error) {
	var out []Resolved
	yes, no := true, false
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		for i, n := range names {
			if i >= 10 {
				break
			}
			res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: n})
			switch {
			case err == nil:
				title := ""
				for _, ch := range res.Chats {
					if c, ok := ch.(*tg.Channel); ok {
						title = c.Title
					}
				}
				for _, us := range res.Users {
					if u, ok := us.(*tg.User); ok && title == "" {
						title = u.FirstName
					}
				}
				out = append(out, Resolved{Username: n, Exists: &yes, Title: title})
			case tgerr.Is(err, "USERNAME_NOT_OCCUPIED", "USERNAME_INVALID"):
				out = append(out, Resolved{Username: n, Exists: &no})
			default:
				out = append(out, Resolved{Username: n, Error: Describe(err).Error()})
			}
		}
		return nil
	})
	return out, err
}

// ---- Поиск постов ----

type Post struct {
	Username string `json:"username"`
	ID       int    `json:"id"`
	Date     int64  `json:"date"`
	Text     string `json:"text"`
	Views    int    `json:"views"`
	HasMedia bool   `json:"has_media"`
}

type PostsResult struct {
	Posts     []Post
	FloodWait int
	Paid      bool
}

// SearchPosts — глобальный поиск публичных постов: по хэштегу бесплатно; по словам — только если Telegram говорит, что запрос сейчас бесплатный.
func SearchPosts(ctx context.Context, acc *Account, hashtag, query string, limit int) (PostsResult, error) {
	var r PostsResult
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		if hashtag == "" {
			flood, err := api.ChannelsCheckSearchPostsFlood(ctx, &tg.ChannelsCheckSearchPostsFloodRequest{Query: query})
			if d, ok := tgerr.AsFloodWait(err); ok {
				r.FloodWait = int(d.Seconds())
				return nil
			}
			if err != nil {
				return err
			}
			if !flood.QueryIsFree {
				r.Paid = true
				return nil
			}
		}
		req := &tg.ChannelsSearchPostsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: min(limit, 100)}
		if hashtag != "" {
			req.SetHashtag(hashtag)
		} else {
			req.SetQuery(query)
		}
		res, err := api.ChannelsSearchPosts(ctx, req)
		if d, ok := tgerr.AsFloodWait(err); ok {
			r.FloodWait = int(d.Seconds())
			return nil
		}
		if err != nil {
			return err
		}
		m, ok := res.AsModified()
		if !ok {
			return nil
		}
		chats := map[int64]*tg.Channel{}
		for _, ch := range m.GetChats() {
			if c, ok := ch.(*tg.Channel); ok {
				chats[c.ID] = c
			}
		}
		for _, mc := range m.GetMessages() {
			msg, ok := mc.(*tg.Message)
			if !ok || msg.ID == 0 {
				continue
			}
			pc, ok := msg.PeerID.(*tg.PeerChannel)
			if !ok {
				continue
			}
			c := chats[pc.ChannelID]
			if c == nil || c.Username == "" {
				continue // только публичные каналы: ссылка t.me/<имя>/<номер> должна открываться
			}
			views, _ := msg.GetViews()
			r.Posts = append(r.Posts, Post{Username: c.Username, ID: msg.ID, Date: int64(msg.Date), Text: msg.Message, Views: views, HasMedia: msg.Media != nil})
		}
		return nil
	})
	return r, err
}

// ---- Проверка канала ----

type ChannelCheck struct {
	Title    string `json:"title"`
	Username string `json:"username"`
	CanPost  bool   `json:"can_post"`
	Role     string `json:"role"`
	Account  string `json:"account"`
}

// CheckChannel — права аккаунта в канале: владелец, администратор с правом публикации и т. д.
func CheckChannel(ctx context.Context, acc *Account, ref string) (ChannelCheck, error) {
	var r ChannelCheck
	err := Run(ctx, acc, nil, func(ctx context.Context, c *telegram.Client) error {
		me, err := c.Self(ctx)
		if err != nil {
			return err
		}
		ch, err := findChannel(ctx, c.API(), ref)
		if err != nil {
			return err
		}
		r.Account = strings.TrimSpace(me.FirstName + " " + me.LastName)
		if me.Username != "" {
			r.Account += " @" + me.Username
		}
		r.Title, r.Username = ch.Title, ch.Username
		rights, hasRights := ch.GetAdminRights()
		switch {
		case ch.Creator:
			r.Role = "владелец"
		case hasRights && rights.PostMessages:
			r.Role = "администратор"
		case hasRights:
			r.Role = "администратор без права публиковать"
		case ch.Left:
			r.Role = "не участник канала"
		default:
			r.Role = "обычный участник"
		}
		r.CanPost = r.Role == "владелец" || r.Role == "администратор"
		return nil
	})
	return r, err
}

// Cancel — убрать посты из «Отложенных» канала.
func Cancel(ctx context.Context, acc *Account, ref string, ids []int) error {
	return RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		ch, err := findChannel(ctx, api, ref)
		if err != nil {
			return err
		}
		_, err = api.MessagesDeleteScheduledMessages(ctx, &tg.MessagesDeleteScheduledMessagesRequest{Peer: inputPeer(ch), ID: ids})
		return err
	})
}

// ---- Чтение чатов (радар лидов) ----

type ChatSpec struct {
	Ref   string `json:"ref"`
	MinID int    `json:"min_id"`
	Limit int    `json:"limit"`
}

type ChatMessage struct {
	ID             int    `json:"id"`
	Date           int64  `json:"date"`
	Text           string `json:"text"`
	SenderID       int64  `json:"sender_id"`
	SenderUsername string `json:"sender_username"`
	SenderName     string `json:"sender_name"`
}

type ChatResult struct {
	Ref      string        `json:"ref"`
	ChatID   int64         `json:"chat_id"`
	Title    string        `json:"title"`
	Username string        `json:"username"`
	Error    string        `json:"error"`
	Messages []ChatMessage `json:"messages"`
}

// ReadChats — новые сообщения групп без вступления; служебные, свои, от ботов и без текста пропускаются.
func ReadChats(ctx context.Context, acc *Account, specs []ChatSpec) ([]ChatResult, error) {
	var out []ChatResult
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		for _, s := range specs {
			r := ChatResult{Ref: s.Ref, Messages: []ChatMessage{}}
			limit := s.Limit
			if limit <= 0 {
				limit = 100
			}
			if s.MinID == 0 {
				limit = min(limit, 30) // первый запуск — только последние 30
			}
			stop, err := readChat(ctx, api, s, limit, &r)
			if err != nil {
				r.Error = err.Error()
			}
			out = append(out, r)
			if stop {
				break
			}
		}
		return nil
	})
	return out, err
}

func readChat(ctx context.Context, api *tg.Client, s ChatSpec, limit int, r *ChatResult) (stop bool, _ error) {
	ch, err := findChannel(ctx, api, s.Ref)
	if err != nil {
		if tgerr.Is(err, "CHANNEL_PRIVATE", "CHAT_ADMIN_REQUIRED") {
			return false, fail("Закрыт для чтения без вступления")
		}
		return false, Describe(err)
	}
	if ch.Broadcast {
		return false, fail("Это канал, а не чат: людей там не видно")
	}
	r.ChatID, r.Title, r.Username = ch.ID, ch.Title, ch.Username
	res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: inputPeer(ch), Limit: limit, MinID: s.MinID})
	if d, ok := tgerr.AsFloodWait(err); ok {
		if d > time.Minute {
			return true, fail("Telegram просит подождать %d с", int(d.Seconds()))
		}
		return false, fail("Telegram просит подождать %d с", int(d.Seconds()))
	}
	if err != nil {
		if tgerr.Is(err, "CHANNEL_PRIVATE", "CHAT_ADMIN_REQUIRED") {
			return false, fail("Закрыт для чтения без вступления")
		}
		return false, Describe(err)
	}
	m, ok := res.AsModified()
	if !ok {
		return false, nil
	}
	users := map[int64]*tg.User{}
	for _, uc := range m.GetUsers() {
		if u, ok := uc.(*tg.User); ok {
			users[u.ID] = u
		}
	}
	for _, mc := range m.GetMessages() {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.Out || msg.Message == "" || msg.ID <= s.MinID {
			continue
		}
		from, ok := msg.FromID.(*tg.PeerUser)
		if !ok {
			continue
		}
		u := users[from.UserID]
		if u != nil && u.Bot {
			continue
		}
		cm := ChatMessage{ID: msg.ID, Date: int64(msg.Date), Text: msg.Message, SenderID: from.UserID}
		if u != nil {
			cm.SenderName = string([]rune(strings.TrimSpace(u.FirstName + " " + u.LastName))[:min(64, len([]rune(strings.TrimSpace(u.FirstName+" "+u.LastName))))])
			cm.SenderUsername = u.Username
		}
		r.Messages = append(r.Messages, cm)
	}
	return false, nil
}
