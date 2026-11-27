package posta

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

// The shared Posta assistant is distinct from user-supplied publishing bots.
// Its credential is never returned by the browser API or copied to channels.
const maxBotConfigKey = "max_service_bot"
const maxBotLinkTTL = 600

type maxServiceBot struct {
	ID int64 `json:"id"`
	Username string `json:"username"`
	TokenEnc string `json:"token_enc"`
	SecretEnc string `json:"secret_enc"`
	Ready bool `json:"ready"`
	SiteURL string `json:"site_url,omitempty"`
}

var maxBotSetupMu sync.Mutex
var maxBotUsername = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,100}$`)
var maxBotLinkToken = regexp.MustCompile(`^[a-f0-9]{32}$`)
var maxBotConfirmCode = regexp.MustCompile(`^[A-F0-9]{8}$`)

func (a *App) maxBotConfig(ctx context.Context) maxServiceBot {
	if a.DB == nil {
		return maxServiceBot{}
	}
	return KV(a,ctx,maxBotConfigKey,maxServiceBot{})
}

func (a *App) maxBotSecrets(bot maxServiceBot) (string,string) {
	if a.Box==nil || bot.ID<=0 || !maxBotUsername.MatchString(bot.Username) {
		return "",""
	}
	token, ok := a.decryptAny(bot.TokenEnc,maxBotConfigKey+":token")
	if !ok { return "","" }
	secret, ok := a.decryptAny(bot.SecretEnc,maxBotConfigKey+":secret")
	if !ok { return "","" }
	return token,secret
}

func maxBotSiteURL(raw string) string {
	p,err:=url.Parse(strings.TrimRight(strings.TrimSpace(raw),"/"))
	if err!=nil || p.Scheme!="https" || p.Hostname()=="" || p.User!=nil || p.RawQuery!="" || p.ForceQuery || p.Fragment!="" || p.Port()!="" || p.Opaque!="" {
		return ""
	}
	host:=strings.ToLower(strings.TrimSuffix(p.Hostname(),"."))
	// MAX requires a publicly reachable TLS domain. Do not accept localhost,
	// IP literals, or a single-label/private network hostname.
	if net.ParseIP(host)!=nil || !strings.Contains(host,".") || strings.HasSuffix(host,".localhost") || strings.HasSuffix(host,".local") || strings.HasSuffix(host,".internal") || strings.HasSuffix(host,".lan") {
		return ""
	}
	return p.String()
}

// A bot-specific, owner-confirmed URL persists across restarts. Existing
// deployments with a valid POSTA_PUBLIC_URL continue to work unchanged.
func (a *App) maxBotSite(bot maxServiceBot) string {
	if bot.SiteURL!="" { return maxBotSiteURL(bot.SiteURL) }
	return maxBotSiteURL(a.Cfg.PublicURL)
}

func (a *App) MaxBotPublic(ctx context.Context, uid string) (jx.M,error) {
	bot:=a.maxBotConfig(ctx)
	token,secret:=a.maxBotSecrets(bot)
	site:=a.maxBotSite(bot)
	ready:=bot.Ready && token!="" && secret!="" && site!=""
	out:=jx.M{"configured":ready,"linked":false,"username":bot.Username,"site_url":site}
	if !ready { return out,nil }
	out["url"]="https://max.ru/"+bot.Username
	var linked bool
	err:=a.DB.Pool.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM max_bot_links WHERE user_id=$1 AND bot_id=$2)`,uid,bot.ID).Scan(&linked)
	out["linked"]=linked
	return out,err
}

func (a *App) MaxBotConfigure(ctx context.Context, token, siteURL string) error {
	return a.maxBotConfigureWith(ctx,token,siteURL,networkHTTP)
}

func (a *App) maxBotConfigureWith(ctx context.Context, token, siteURL string, request networkRequestFunc) error {
	maxBotSetupMu.Lock()
	defer maxBotSetupMu.Unlock()
	token=strings.TrimSpace(token)
	if a.DB==nil || a.Box==nil { return Bad("Хранилище секретов недоступно.") }
	old:=a.maxBotConfig(ctx)
	site:=a.maxBotSite(old)
	if strings.TrimSpace(siteURL)!="" { site=maxBotSiteURL(siteURL) }
	if site=="" { return Bad("Укажите публичный HTTPS-адрес сайта Posta в настройке бота MAX. Локальный адрес не подходит.") }
	if old.Ready && a.maxBotSite(old)!="" && site!=a.maxBotSite(old) {
		return Conflict("У бота уже настроен другой адрес вебхука. Сначала нужно перенести действующую подписку.")
	}
	if token=="" { token,_=a.maxBotSecrets(old) }
	if len(token)<10 || len(token)>1024 || strings.ContainsAny(token," \t\r\n") { return Bad("Укажите токен бота MAX.") }
	ctx,cancel:=context.WithTimeout(ctx,25*time.Second)
	defer cancel()
	headers:=map[string]string{"Authorization":token}
	r,err:=request(ctx,"GET",maxAPI+"/me",headers,nil)
	var identity struct { ID int64 `json:"user_id"`; Username string `json:"username"`; IsBot bool `json:"is_bot"` }
	if err!=nil || r==nil || r.Status!=200 || json.Unmarshal(r.Raw,&identity)!=nil || !identity.IsBot || identity.ID<=0 || !maxBotUsername.MatchString(identity.Username) {
		return Bad("Не удалось подтвердить бота MAX. Проверьте токен и доступность API.")
	}
	if old.ID!=0 && old.ID!=identity.ID { return Conflict("Уже настроен другой общий бот. Замену бота нужно выполнить отдельно, чтобы не потерять привязки.") }
	_,secret:=a.maxBotSecrets(old)
	if secret=="" { secret=randHex(32) }
	bot:=maxServiceBot{ID:identity.ID,Username:identity.Username,SiteURL:site,TokenEnc:a.Box.Encrypt(token,maxBotConfigKey+":token"),SecretEnc:a.Box.Encrypt(secret,maxBotConfigKey+":secret")}
	// Save credentials first so the webhook can authenticate any provider probe.
	if err=a.KVSet(ctx,maxBotConfigKey,bot,0);err!=nil { return err }
	r,err=request(ctx,"POST",maxAPI+"/subscriptions",withCT(headers,ctJSON),jsonBody(jx.M{"url":site+"/max-bot/webhook","secret":secret,"update_types":[]string{"bot_started","message_created"}}))
	if err!=nil || r==nil || r.Status!=200 || !jx.Bool(r.Data,"success") {
		return Bad("Токен принят, но MAX не подтвердил вебхук. Проверьте доступность HTTPS-сайта и повторите настройку.")
	}
	bot.Ready=true
	return a.KVSet(ctx,maxBotConfigKey,bot,0)
}

func (a *App) MaxBotLinkStart(ctx context.Context, uid string) (jx.M,error) {
	bot:=a.maxBotConfig(ctx)
	token,secret:=a.maxBotSecrets(bot)
	if !bot.Ready || token=="" || secret=="" { return nil,Bad("Бот MAX пока не настроен.") }
	linkToken:=randHex(16)
	err:=a.DB.Tx(ctx,func(tx pgx.Tx) error {
		var locked int
		if err:=tx.QueryRow(ctx,`SELECT 1 FROM users WHERE id=$1 FOR UPDATE`,uid).Scan(&locked);err!=nil { return err }
		var linked bool
		if err:=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM max_bot_links WHERE user_id=$1 AND bot_id=$2)`,uid,bot.ID).Scan(&linked);err!=nil { return err }
		if linked { return Conflict("Бот уже привязан к профилю.") }
		tag,err:=tx.Exec(ctx,`INSERT INTO max_bot_link_requests(user_id,bot_id,token_hash,created_at)
			VALUES($1,$2,$3,$4) ON CONFLICT(user_id) DO UPDATE SET bot_id=EXCLUDED.bot_id,token_hash=EXCLUDED.token_hash,
			created_at=EXCLUDED.created_at,max_user_id=NULL,attempts=0 WHERE max_bot_link_requests.created_at <= $4-5`,
			uid,bot.ID,sha256Hex(linkToken),now())
		if err!=nil { return err }
		if tag.RowsAffected()!=1 { return Conflict("Подождите несколько секунд перед повторной привязкой.") }
		return nil
	})
	if err!=nil { return nil,err }
	return jx.M{"token":linkToken,"url":"https://max.ru/"+bot.Username+"?start=link_"+linkToken,"expires_in":maxBotLinkTTL},nil
}

// Confirmation is deliberately separate from opening the deep link. A stolen
// or forwarded start URL is insufficient to link an account: the browser
// session that initiated it must submit a code shown only in the MAX dialog.
func maxBotCode(secret, token string, id int64) string {
	mac:=hmac.New(sha256.New,[]byte(secret))
	fmt.Fprintf(mac,"max-link-v1:%s:%d",token,id)
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)[:4]))
}

func (a *App) maxBotClaimLink(ctx context.Context, botID, maxID int64, token string) (bool,error) {
	if maxID<=0 || !maxBotLinkToken.MatchString(token) { return false,nil }
	tag,err:=a.DB.Pool.Exec(ctx,`UPDATE max_bot_link_requests SET max_user_id=$1 WHERE token_hash=$2 AND bot_id=$3
		AND created_at>$4 AND attempts<5 AND (max_user_id IS NULL OR max_user_id=$1)`,
		maxID,sha256Hex(token),botID,now()-maxBotLinkTTL)
	return err==nil && tag.RowsAffected()==1,err
}

func (a *App) MaxBotLinkConfirm(ctx context.Context, uid, token, code string) error {
	bot:=a.maxBotConfig(ctx)
	credential,secret:=a.maxBotSecrets(bot)
	code=strings.ToUpper(strings.TrimSpace(code))
	if !bot.Ready || credential=="" || secret=="" || !maxBotLinkToken.MatchString(token) || !maxBotConfirmCode.MatchString(code) { return Bad("Неверный или устаревший код привязки.") }
	var result error
	err:=a.DB.Tx(ctx,func(tx pgx.Tx) error {
		var locked int
		if err:=tx.QueryRow(ctx,`SELECT 1 FROM users WHERE id=$1 FOR UPDATE`,uid).Scan(&locked);err!=nil { return err }
		var maxID int64
		var attempts int
		err:=tx.QueryRow(ctx,`SELECT COALESCE(max_user_id,0),attempts FROM max_bot_link_requests
			WHERE user_id=$1 AND bot_id=$2 AND token_hash=$3 AND created_at>$4 FOR UPDATE`,
			uid,bot.ID,sha256Hex(token),now()-maxBotLinkTTL).Scan(&maxID,&attempts)
		if errors.Is(err,pgx.ErrNoRows) || (err==nil && (maxID<=0 || attempts>=5)) { result=Bad("Откройте свежую ссылку в MAX и получите код.");return nil }
		if err!=nil { return err }
		if subtle.ConstantTimeCompare([]byte(code),[]byte(maxBotCode(secret,token,maxID)))!=1 {
			_,err=tx.Exec(ctx,`UPDATE max_bot_link_requests SET attempts=attempts+1 WHERE user_id=$1`,uid)
			result=Bad("Код не совпадает. Проверьте сообщение от бота.")
			return err
		}
		tag,err:=tx.Exec(ctx,`INSERT INTO max_bot_links(user_id,bot_id,max_user_id,linked_at) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`,uid,bot.ID,maxID,now())
		if err!=nil { return err }
		if tag.RowsAffected()!=1 { result=Conflict("Этот аккаунт MAX или профиль уже привязан. Сначала отключите прежнюю привязку.");return nil }
		_,err=tx.Exec(ctx,`DELETE FROM max_bot_link_requests WHERE user_id=$1`,uid)
		return err
	})
	if err!=nil { return err }
	return result
}

func (a *App) MaxBotUnlink(ctx context.Context,uid string) error {
	return a.DB.Tx(ctx,func(tx pgx.Tx) error {
		var locked int
		if err:=tx.QueryRow(ctx,`SELECT 1 FROM users WHERE id=$1 FOR UPDATE`,uid).Scan(&locked);err!=nil { return err }
		if _,err:=tx.Exec(ctx,`DELETE FROM max_bot_link_requests WHERE user_id=$1`,uid);err!=nil { return err }
		_,err:=tx.Exec(ctx,`DELETE FROM max_bot_links WHERE user_id=$1`,uid)
		return err
	})
}

type maxBotUser struct {
	ID int64 `json:"user_id"`
	IsBot bool `json:"is_bot"`
}
type maxBotUpdate struct {
	Type string `json:"update_type"`
	User maxBotUser `json:"user"`
	Payload string `json:"payload"`
	Message struct {
		Sender maxBotUser `json:"sender"`
		Recipient struct { Type string `json:"chat_type"` } `json:"recipient"`
		Body struct { Text string `json:"text"` } `json:"body"`
	} `json:"message"`
}

// Only private conversations may cause a response or reveal account status.
func maxBotPrivateCommand(update maxBotUpdate) (int64,string) {
	switch update.Type {
	case "bot_started":
		if update.User.ID>0 && !update.User.IsBot {
			return update.User.ID,"/start "+update.Payload
		}
	case "message_created":
		if update.Message.Recipient.Type=="dialog" && update.Message.Sender.ID>0 && !update.Message.Sender.IsBot {
			return update.Message.Sender.ID,strings.TrimSpace(update.Message.Body.Text)
		}
	}
	return 0,""
}

func (a *App) MaxBotWebhookAuthorized(ctx context.Context, supplied string) bool {
	bot:=a.maxBotConfig(ctx)
	_,secret:=a.maxBotSecrets(bot)
	return secret!="" && subtle.ConstantTimeCompare([]byte(secret),[]byte(supplied))==1
}

func (a *App) MaxBotHandleWebhook(ctx context.Context, supplied string, raw []byte) error {
	bot:=a.maxBotConfig(ctx)
	token,secret:=a.maxBotSecrets(bot)
	if secret=="" || subtle.ConstantTimeCompare([]byte(secret),[]byte(supplied))!=1 { return Bad("Неверный секрет вебхука.") }
	if !bot.Ready { return nil }
	var update maxBotUpdate
	if err:=json.Unmarshal(raw,&update);err!=nil { return Bad("Некорректное событие MAX.") }
	id,command:=maxBotPrivateCommand(update)
	if id<=0 { return nil }
	ctx,cancel:=context.WithTimeout(ctx,15*time.Second)
	defer cancel()
	return a.maxBotReply(ctx,bot,token,secret,id,command,networkHTTP)
}

func (a *App) maxBotReply(ctx context.Context, bot maxServiceBot, token,secret string, id int64, command string, request networkRequestFunc) error {
	site:=a.maxBotSite(bot)
	if site=="" { return Bad("Публичный адрес не настроен.") }
	text:="Posta — поиск контента и автопостинг.\n\n/start — открыть Posta\n/status — состояние вашего профиля\n/help — помощь\n\nЧтобы связать аккаунты, откройте «Личный кабинет → Соцсети → Бот Posta в MAX»."
	if strings.HasPrefix(command,"/start link_") {
		linkToken:=strings.TrimPrefix(command,"/start link_")
		claimed,err:=a.maxBotClaimLink(ctx,bot.ID,id,linkToken)
		if err!=nil { return err }
		if claimed {
			text="Код привязки Posta: "+maxBotCode(secret,linkToken,id)+"\n\nВведите его только в своём открытом личном кабинете Posta. Никому не пересылайте код. Если вы не начинали привязку, проигнорируйте сообщение."
		} else { text="Ссылка устарела. Начните привязку заново в личном кабинете Posta." }
	} else if command=="/status" {
		var uid string
		err:=a.DB.Pool.QueryRow(ctx,`SELECT user_id FROM max_bot_links WHERE bot_id=$1 AND max_user_id=$2`,bot.ID,id).Scan(&uid)
		if errors.Is(err,pgx.ErrNoRows) { text="Аккаунт ещё не привязан. Откройте «Личный кабинет → Соцсети → Бот Posta в MAX»." } else if err!=nil { return err } else {
			var pool,queued,failed int
			err=a.DB.Pool.QueryRow(ctx,`SELECT
				(SELECT count(*) FROM pool_items WHERE user_id=$1),
				(SELECT count(*) FROM outbox WHERE user_id=$1 AND status IN ('queued','sending','telegram')),
				(SELECT count(*) FROM outbox WHERE user_id=$1 AND status='failed')`,uid).Scan(&pool,&queued,&failed)
			if err!=nil { return err }
			text=fmt.Sprintf("Ваш профиль Posta\n\nВ пуле контента: %d\nОжидают публикации: %d\nОшибки публикации: %d",pool,queued,failed)
		}
	} else if command!="/start" && command!="/start " && command!="/help" {
		return nil
	}
	body:=jx.M{"text":text,"attachments":[]jx.M{{"type":"inline_keyboard","payload":jx.M{"buttons":[][]jx.M{{{"type":"link","text":"Открыть Posta","url":site+"/"}}}}}}}
	r,err:=request(ctx,"POST",maxAPI+"/messages?user_id="+strconv.FormatInt(id,10),map[string]string{"Authorization":token,"Content-Type":ctJSON},jsonBody(body))
	if err!=nil || r==nil || r.Status!=200 || jx.Str(jx.Map(jx.Map(r.Data,"message"),"body"),"mid")=="" { return Bad("MAX не подтвердил отправку сообщения.") }
	return nil
}
