package posta

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"posta/internal/jx"
)

// ---- YouTube Shorts ----

var youtubeOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://accounts.google.com/o/oauth2/v2/auth?" + url.Values{"client_id": {a.NetworkAppKey("YOUTUBE_CLIENT_ID")}, "redirect_uri": {redirect},
			"response_type": {"code"}, "scope": {"https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly"},
			"access_type": {"offline"}, "prompt": {"consent"}, "state": {state}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://oauth2.googleapis.com/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"client_id": {a.NetworkAppKey("YOUTUBE_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("YOUTUBE_CLIENT_SECRET")}}, "", "", "Google не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		return a.refreshTokenRequest(ctx, s, "https://oauth2.googleapis.com/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"client_id": {a.NetworkAppKey("YOUTUBE_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("YOUTUBE_CLIENT_SECRET")}}, "", "", "Google не выдал токен", nil)
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://www.googleapis.com/youtube/v3/channels?part=snippet&mine=true", bearer(s["access_token"]), nil)
		d, err := networkExpect(r, err, []int{200}, "Канал YouTube не получен")
		if err != nil {
			return nil, err
		}
		items := jx.Maps(d, "items")
		if len(items) == 0 {
			return nil, Bad("У аккаунта нет канала YouTube.")
		}
		title := jx.Str(jx.Map(items[0], "snippet"), "title")
		if title == "" {
			title = "YouTube"
		}
		return &NetResource{Ref: jx.Str(items[0], "id"), Title: title, Meta: jx.M{"privacy": "private"}}, nil
	},
}

func youtubePublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	return youtubePublishWith(ctx,c,p,networkHTTP,waitNetworkContext)
}

// ---- Pinterest ----

var pinterestOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.pinterest.com/oauth/?" + url.Values{"client_id": {a.NetworkAppKey("PINTEREST_CLIENT_ID")}, "redirect_uri": {redirect},
			"response_type": {"code"}, "scope": {"user_accounts:read,boards:read,pins:read,pins:write"}, "state": {state}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://api.pinterest.com/v5/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"continuous_refresh": {"true"}}, "PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET", "Pinterest не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		return a.refreshTokenRequest(ctx, s, "https://api.pinterest.com/v5/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"continuous_refresh": {"true"}}, "PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET", "Pinterest не выдал токен", nil)
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.pinterest.com/v5/user_account", bearer(s["access_token"]), nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт Pinterest не получен")
		if err != nil {
			return nil, err
		}
		r, err = networkHTTP(ctx, "GET", "https://api.pinterest.com/v5/boards", bearer(s["access_token"]), nil)
		boards, err := networkExpect(r, err, []int{200}, "Доски Pinterest не получены")
		if err != nil {
			return nil, err
		}
		items := jx.Maps(boards, "items")
		if len(items) == 0 {
			return nil, Bad("У аккаунта Pinterest нет досок: создайте доску и подключите снова.")
		}
		return &NetResource{Ref: jx.Str(me, "username"), Title: "@" + jx.Str(me, "username"), Meta: jx.M{"board_id": jx.Str(items[0], "id")}}, nil
	},
}

func pinterestPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	cover:=func(ctx context.Context,path string)[]byte{
		board:=a.videoStoryboardContext(ctx,path,1)
		if board==nil||len(board.Frames)==0{return nil}
		return board.Frames[0]
	}
	return pinterestPublishWith(ctx,c,p,networkHTTP,cover,waitNetworkContext)
}

// ---- Tumblr ----

var tumblrOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.tumblr.com/oauth2/authorize?" + url.Values{"client_id": {a.NetworkAppKey("TUMBLR_CLIENT_ID")}, "response_type": {"code"},
			"scope": {"basic write offline_access"}, "state": {state}, "redirect_uri": {redirect}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://api.tumblr.com/v2/oauth2/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"client_id": {a.NetworkAppKey("TUMBLR_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("TUMBLR_CLIENT_SECRET")}}, "", "", "Tumblr не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		return a.refreshTokenRequest(ctx, s, "https://api.tumblr.com/v2/oauth2/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"client_id": {a.NetworkAppKey("TUMBLR_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("TUMBLR_CLIENT_SECRET")}}, "", "", "Tumblr не выдал токен", nil)
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.tumblr.com/v2/user/info", bearer(s["access_token"]), nil)
		d, err := networkExpect(r, err, []int{200}, "Блоги Tumblr не получены")
		if err != nil {
			return nil, err
		}
		blogs := jx.Maps(jx.Map(jx.Map(d, "response"), "user"), "blogs")
		var blog jx.M
		for _, b := range blogs {
			if jx.Bool(b, "primary") {
				blog = b
			}
		}
		if blog == nil && len(blogs) > 0 {
			blog = blogs[0]
		}
		if blog == nil {
			return nil, Bad("У аккаунта Tumblr нет блогов.")
		}
		title := jx.Str(blog, "title")
		if title == "" {
			title = jx.Str(blog, "name")
		}
		return &NetResource{Ref: jx.Str(blog, "name"), Title: title, Meta: jx.M{"blog": jx.Str(blog, "name") + ".tumblr.com"}}, nil
	},
}

func tumblrPublish(a *App,ctx context.Context,c *Channel,p NetPost)(*NetSent,error){
	return tumblrPublishWith(ctx,c,p,networkHTTP)
}

// ---- LinkedIn ----

var linkedinOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.linkedin.com/oauth/v2/authorization?" + url.Values{"response_type": {"code"}, "client_id": {a.NetworkAppKey("LINKEDIN_CLIENT_ID")},
			"redirect_uri": {redirect}, "state": {state}, "scope": {"openid profile w_member_social"}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		t, err := a.tokenRequest(ctx, "https://www.linkedin.com/oauth/v2/accessToken", url.Values{"grant_type": {"authorization_code"}, "code": {code},
			"client_id": {a.NetworkAppKey("LINKEDIN_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("LINKEDIN_CLIENT_SECRET")}, "redirect_uri": {redirect}}, "", "", "LinkedIn не выдал токен", nil)
		if t != nil {
			delete(t, "refresh_token")
		}
		return t, err
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.linkedin.com/v2/userinfo", bearer(s["access_token"]), nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт LinkedIn не получен")
		if err != nil {
			return nil, err
		}
		if jx.Str(me, "sub") == "" {
			return nil, Bad("Аккаунт LinkedIn не определён.")
		}
		title := jx.Str(me, "name")
		if title == "" {
			title = "LinkedIn"
		}
		return &NetResource{Ref: jx.Str(me, "sub"), Title: title, Meta: jx.M{}}, nil
	},
}

func linkedinPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if strings.TrimSpace(p.Text) == "" {
		return nil, Bad("LinkedIn: пост без текста.")
	}
	body := jx.M{"author": "urn:li:person:" + c.Ref, "commentary": p.Text, "visibility": "PUBLIC",
		"distribution":   jx.M{"feedDistribution": "MAIN_FEED", "targetEntities": []any{}, "thirdPartyDistributionChannels": []any{}},
		"lifecycleState": "PUBLISHED", "isReshareDisabledByAuthor": false}
	h := withCT(bearer(c.Secrets["access_token"]), ctJSON)
	h["Linkedin-Version"], h["X-Restli-Protocol-Version"] = "202511", "2.0.0"
	r, err := networkHTTP(ctx, "POST", "https://api.linkedin.com/rest/posts", h, jsonBody(body))
	if _, err := networkExpect(r, err, []int{201}, "Пост не опубликован"); err != nil {
		return nil, err
	}
	urn := r.Headers.Get("X-Restli-Id")
	u := ""
	if urn != "" {
		u = "https://www.linkedin.com/feed/update/" + urn
	}
	return &NetSent{URL: u, MessageIDs: []string{urn}}, nil
}

// ---- X ----

var xOAuth = &OAuthFlow{
	PKCE: true,
	Authorize: func(a *App, state, redirect, verifier string) string {
		sum := sha256.Sum256([]byte(verifier))
		return "https://x.com/i/oauth2/authorize?" + url.Values{"response_type": {"code"}, "client_id": {a.NetworkAppKey("X_CLIENT_ID")}, "redirect_uri": {redirect},
			"scope": {"tweet.write users.read tweet.read media.write offline.access"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
			"code_challenge_method": {"S256"}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, verifier string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://api.x.com/2/oauth2/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"code_verifier": {verifier}, "client_id": {a.NetworkAppKey("X_CLIENT_ID")}}, "X_CLIENT_ID", "X_CLIENT_SECRET", "X не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		return a.refreshTokenRequest(ctx, s, "https://api.x.com/2/oauth2/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"client_id": {a.NetworkAppKey("X_CLIENT_ID")}}, "X_CLIENT_ID", "X_CLIENT_SECRET", "X не выдал токен", nil)
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.x.com/2/users/me", bearer(s["access_token"]), nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт X не получен")
		if err != nil {
			return nil, err
		}
		user := jx.Map(me, "data")
		if jx.Str(user, "id") == "" {
			return nil, Bad("Аккаунт X не определён.")
		}
		return &NetResource{Ref: jx.Str(user, "id"), Title: "@" + jx.Str(user, "username"), Meta: jx.M{"username": jx.Str(user, "username")}}, nil
	},
}

func xPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	return xPublishWith(ctx, c, p, networkHTTP, waitNetworkContext)
}

// ---- Reddit ----

const redditUA = "web:posta:1.0 (автопостинг)"

var redditOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.reddit.com/api/v1/authorize?" + url.Values{"client_id": {a.NetworkAppKey("REDDIT_CLIENT_ID")}, "response_type": {"code"}, "state": {state},
			"redirect_uri": {redirect}, "duration": {"permanent"}, "scope": {"identity submit"}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://www.reddit.com/api/v1/access_token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}},
			"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "Reddit не выдал токен", map[string]string{"User-Agent": redditUA})
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		return a.refreshTokenRequest(ctx, s, "https://www.reddit.com/api/v1/access_token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]}},
			"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "Reddit не выдал токен", map[string]string{"User-Agent": redditUA})
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://oauth.reddit.com/api/v1/me", map[string]string{"Authorization": "bearer " + s["access_token"], "User-Agent": redditUA}, nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт Reddit не получен")
		if err != nil {
			return nil, err
		}
		name := jx.Str(me, "name")
		if name == "" {
			return nil, Bad("Аккаунт Reddit не определён.")
		}
		return &NetResource{Ref: name, Title: "u/" + name, Meta: jx.M{"subreddit": "u_" + strings.ToLower(name)}}, nil
	},
}

func redditPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return nil, Bad("Reddit: пост без текста.")
	}
	title, body := titleAndBody(text)
	form := url.Values{"api_type": {"json"}, "kind": {"self"}, "sr": {jx.Str(c.Meta, "subreddit")}, "title": {trimRunes(title, 300)}, "text": {body}}
	r, err := networkHTTP(ctx, "POST", "https://oauth.reddit.com/api/submit", map[string]string{"Authorization": "bearer " + c.Secrets["access_token"], "User-Agent": redditUA,
		"Content-Type": ctForm}, formBody(form))
	d, err := networkExpect(r, err, []int{200}, "Пост не опубликован")
	if err != nil {
		return nil, err
	}
	j := jx.Map(d, "json")
	if errs := jx.List(j, "errors"); len(errs) > 0 {
		raw, _ := json.Marshal(errs)
		return nil, fmt.Errorf("Reddit: %s.", trimRunes(string(raw), 200))
	}
	data := jx.Map(j, "data")
	return &NetSent{URL: jx.Str(data, "url"), MessageIDs: []string{jx.Str(data, "name")}}, nil
}
