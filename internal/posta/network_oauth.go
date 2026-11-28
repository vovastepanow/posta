package posta

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"posta/internal/jx"
)

// ---- OAuth ----

const (
	networkStateSeconds  = 900
	networkRefreshMargin = 120
)

func (a *App) NetworkRedirectURI() string {
	base := a.Cfg.PublicURL
	if base == "" {
		base = "http://localhost"
	}
	return base + "/index.php?action=network_oauth_callback"
}

func (a *App) NetworkOAuthStateCreate(ctx context.Context, uid, platform, verifier string) (string,error) {
	state := randHex(16)
	if uid==""{return "",Bad("Требуется вход.")}
	if err:=a.KVSet(ctx, "netoauth:"+state, jx.M{"uid": uid, "platform": platform, "verifier": verifier, "at": now()}, networkStateSeconds*time.Second);err!=nil{return "",err}
	return state,nil
}

var stateRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// A callback may consume its state only once, and only in the profile
// which initiated authorization. DELETE RETURNING is atomic across processes.
func (a *App) NetworkOAuthStateTake(ctx context.Context, state, uid string) jx.M {
	if !stateRe.MatchString(state)||uid==""{return nil}
	var raw []byte
	err:=a.DB.Pool.QueryRow(ctx,`DELETE FROM kv WHERE key=$1 AND expires_at>$2 AND value->>'uid'=$3 RETURNING value`,"netoauth:"+state,now(),uid).Scan(&raw)
	if err!=nil{return nil}
	d:=jx.Unmarshal(raw)
	if d==nil||now()-jx.Int(d,"at")>networkStateSeconds{return nil}
	return d
}

func PKCEVerifier() string {
	b := make([]byte, 48)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func expiresAt(t jx.M) string {
	in := jx.Int(t, "expires_in")
	if in == 0 {
		in = 3600
	}
	return strconv.FormatInt(now()+in, 10)
}

// networkRefreshChannel — токен, который скоро истечёт, обновляется до отправки и сохраняется в ресурс.
func (a *App) networkRefreshChannel(ctx context.Context, n *Network, c *Channel) error {
	if n.OAuth == nil || n.OAuth.Refresh == nil || (!n.OAuth.RefreshWithAccessToken && c.Secrets["refresh_token"] == "") {
		return nil
	}
	exp, _ := strconv.ParseInt(c.Secrets["expires_at"], 10, 64)
	margin:=int64(networkRefreshMargin)
	if n.OAuth.RefreshWithAccessToken { margin=7*24*60*60; if exp<=now(){return fmt.Errorf("Срок действия токена истёк; подключите аккаунт заново: %w",ErrNetworkRejected)} }
	if exp > now()+margin {
		return nil
	}
	fresh, err := n.OAuth.Refresh(a, ctx, c.Secrets)
	if err != nil {
		return err
	}
	c.Secrets = fresh
	_, err = a.User(ctx, c.UserID, nil).ChannelSave(c)
	return err
}

func (a *App) tokenRequest(ctx context.Context, tokenURL string, form url.Values, basicID, basicSecret, what string, extra map[string]string) (map[string]string, error) {
	h := map[string]string{"Content-Type": ctForm}
	if basicID != "" {
		h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(a.NetworkAppKey(basicID)+":"+a.NetworkAppKey(basicSecret)))
	}
	for k, v := range extra {
		h[k] = v
	}
	r, err := networkHTTP(ctx, "POST", tokenURL, h, formBody(form))
	t, err := networkExpect(r, err, []int{200}, what)
	if err != nil {
		return nil, err
	}
	if jx.Str(t,"error")!=""||strings.TrimSpace(jx.Str(t,"access_token"))==""{
		return nil,Bad("%s: сервис не выдал токен доступа.",what)
	}
	return map[string]string{"access_token": jx.Str(t, "access_token"), "refresh_token": jx.Str(t, "refresh_token"), "expires_at": expiresAt(t)}, nil
}

func keepRefresh(old, fresh map[string]string) map[string]string {
	if fresh["refresh_token"] == "" {
		fresh["refresh_token"] = old["refresh_token"]
	}
	return fresh
}
