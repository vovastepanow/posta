package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
	"posta/internal/jx"
)

var metaDigits = regexp.MustCompile(`^[0-9]{5,25}$`)
var metaVersion = regexp.MustCompile(`^v[0-9]{1,2}\.[0-9]$`)
var instagramHandle = regexp.MustCompile(`^[A-Za-z0-9_.]{1,30}$`)

func instagramProfileName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "www.instagram.com" && u.Hostname() != "instagram.com") {
		return ""
	}
	name := strings.Trim(u.Path, "/")
	if !instagramHandle.MatchString(name) || contains([]string{"p", "reel", "reels", "stories", "explore", "accounts", "direct"}, strings.ToLower(name)) {
		return ""
	}
	return strings.ToLower(name)
}

func facebookPageID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || (u.Hostname() != "www.facebook.com" && u.Hostname() != "facebook.com") {
		return ""
	}
	id := strings.Trim(u.Path, "/")
	if !metaDigits.MatchString(id) {
		return ""
	}
	return id
}

func (u *U) metaGraphRead(path string, params url.Values, tokenName string) ([]byte, error) {
	token := u.NetworkAppKey(tokenName)
	if token == "" {
		return nil, Bad("Meta API: токен не настроен.")
	}
	version := u.NetworkAppKey("META_GRAPH_API_VERSION")
	if version == "" {
		version = "v24.0"
	}
	if !metaVersion.MatchString(version) {
		return nil, Bad("Meta API: неверная версия API.")
	}
	ctx, cancel := context.WithTimeout(u.Ctx, 20*time.Second)
	defer cancel()
	r, err := networkHTTP(ctx, "GET", "https://graph.facebook.com/"+version+"/"+path+"?"+params.Encode(), map[string]string{"Authorization": "Bearer " + token}, nil)
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		return nil, fmt.Errorf("Meta API: ответ %d; проверьте токен и права", r.Status)
	}
	return r.Raw, nil
}

type instagramPost struct {
	ID           string `json:"id"`
	Caption      string `json:"caption"`
	MediaType    string `json:"media_type"`
	MediaURL     string `json:"media_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	Permalink    string `json:"permalink"`
	Timestamp    string `json:"timestamp"`
	Children     struct {
		Data []struct {
			MediaType    string `json:"media_type"`
			MediaURL     string `json:"media_url"`
			ThumbnailURL string `json:"thumbnail_url"`
		} `json:"data"`
	} `json:"children"`
}

func instagramBusinessPosts(data []byte, username string, count int, withText bool) ([]jx.M, error) {
	var response struct {
		Business struct {
			Username string `json:"username"`
			Media    struct {
				Data []instagramPost `json:"data"`
			} `json:"media"`
		} `json:"business_discovery"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	if !strings.EqualFold(response.Business.Username, username) {
		return nil, Bad("Instagram: ответ другого профиля.")
	}
	var out []jx.M
	for _, p := range response.Business.Media.Data {
		link, err := url.Parse(p.Permalink)
		if err != nil || link.Scheme != "https" || (link.Hostname() != "www.instagram.com" && link.Hostname() != "instagram.com") || p.ID == "" {
			continue
		}
		at := ParseFeedTime(p.Timestamp)
		if at == 0 {
			continue
		}
		image, video := "", ""
		switch p.MediaType {
		case "IMAGE", "CAROUSEL_ALBUM":
			image = p.MediaURL
		case "VIDEO":
			image, video = p.ThumbnailURL, p.MediaURL
		}
		if image == "" && video == "" && len(p.Children.Data) == 0 && (!withText || runeLen(strings.TrimSpace(p.Caption)) < 120) {
			continue
		}
		post := searchPost("Instagram", "instagram:"+username, "https://www.instagram.com/"+username+"/", trimRunes(p.Caption, 80), p.Caption, image, video, p.Permalink, at)
		var media []jx.M
		for _, child := range p.Children.Data {
			switch child.MediaType {
			case "IMAGE":
				if strings.HasPrefix(child.MediaURL, "https://") {
					media = append(media, jx.M{"image": child.MediaURL, "video": ""})
				}
			case "VIDEO":
				if strings.HasPrefix(child.MediaURL, "https://") {
					media = append(media, jx.M{"image": child.ThumbnailURL, "video": child.MediaURL})
				}
			}
		}
		if len(media) > 0 {
			setPostMedia(post, media)
			post["text_post"] = false
		}
		out = append(out, post)
		if len(out) >= count {
			break
		}
	}
	return out, nil
}

func (u *U) instagramReadProfile(username string, count int, withText bool) ([]jx.M, error) {
	id := u.NetworkAppKey("INSTAGRAM_BUSINESS_ACCOUNT_ID")
	if metaDigits.MatchString(id) && u.NetworkAppKey("INSTAGRAM_ACCESS_TOKEN") != "" {
		fields := "business_discovery.username(" + username + "){username,media.limit(50){id,caption,media_type,media_url,thumbnail_url,permalink,timestamp,children{media_type,media_url,thumbnail_url}}}"
		if body, err := u.metaGraphRead(id, url.Values{"fields": {fields}}, "INSTAGRAM_ACCESS_TOKEN"); err == nil {
			if posts, err := instagramBusinessPosts(body, username, count, withText); err == nil && len(posts) > 0 {
				return posts, nil
			}
		}
	}
	body, err := u.publicSocialHTML("https://www.instagram.com/" + username + "/")
	if err != nil {
		return nil, err
	}
	posts := instagramPublicParse(body, username, count, withText)
	if len(posts) == 0 {
		body, err = u.publicSocialBrowser("https://www.instagram.com/" + username + "/")
		if err != nil {
			return nil, err
		}
		posts = instagramPublicParse(body, username, count, withText)
		if len(posts) == 0 {
			posts = instagramPublicDOMPosts(body, username, count)
			// Keep only posts whose individual public page was accessible. The
			// profile grid alone does not provide the post text or date.
			var detailed []jx.M
			for _, post := range posts[:min(2, len(posts))] {
				detail, err := u.publicSocialBrowser(jx.Str(post, "url"))
				if err != nil {
					break
				}
				if !instagramPublicEnrichPost(detail, post) {
					break
				}
				detailed = append(detailed, post)
			}
			posts = detailed
		}
	}
	if len(posts) == 0 {
		return nil, Bad("Instagram: публичная страница не содержит доступных постов; возможно, требуется вход.")
	}
	return posts, nil
}

func facebookPagePosts(data []byte, pageID string, count int, withText bool) ([]jx.M, error) {
	var response struct {
		Data []struct {
			ID        string `json:"id"`
			Message   string `json:"message"`
			Created   string `json:"created_time"`
			Permalink string `json:"permalink_url"`
			Picture   string `json:"full_picture"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, p := range response.Data {
		link, err := url.Parse(p.Permalink)
		if err != nil || link.Scheme != "https" || (link.Hostname() != "www.facebook.com" && link.Hostname() != "facebook.com") || !strings.HasPrefix(p.ID, pageID+"_") {
			continue
		}
		at := ParseFeedTime(p.Created)
		if at == 0 || (p.Picture == "" && (!withText || runeLen(strings.TrimSpace(p.Message)) < 120)) {
			continue
		}
		out = append(out, searchPost("Facebook", "facebook:"+pageID, "https://www.facebook.com/"+pageID, trimRunes(p.Message, 80), p.Message, p.Picture, "", p.Permalink, at))
		if len(out) >= count {
			break
		}
	}
	return out, nil
}

func (u *U) facebookReadPage(id string, count int, withText bool) ([]jx.M, error) {
	if id != u.NetworkAppKey("FACEBOOK_PAGE_ID") {
		return nil, Bad("Facebook: токен настроен для другой страницы.")
	}
	body, err := u.metaGraphRead(id+"/posts", url.Values{"fields": {"id,message,created_time,permalink_url,full_picture"}, "limit": {"50"}}, "FACEBOOK_PAGE_ACCESS_TOKEN")
	if err != nil {
		return nil, err
	}
	return facebookPagePosts(body, id, count, withText)
}

func instagramSearchCandidates(page string) []DiscoveryCandidate {
	z := html.NewTokenizer(strings.NewReader(page))
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	for len(out) < 20 {
		t := z.Next()
		if t == html.ErrorToken {
			break
		}
		if t != html.StartTagToken && t != html.SelfClosingTagToken {
			continue
		}
		tok := z.Token()
		if tok.Data != "a" {
			continue
		}
		for _, a := range tok.Attr {
			if a.Key != "href" {
				continue
			}
			username := instagramProfileName(a.Val)
			if username != "" && !seen[username] {
				seen[username] = true
				out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://www.instagram.com/" + username + "/", Info: jx.M{"title": "@" + username}})
			}
		}
	}
	return out
}

func (u *U) discoveryFindMeta(queries []string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	{
		for _, q := range queries[:min(2, len(queries))] {
			page, err := u.braveSearchPage("site:instagram.com " + q)
			if err != nil {
				u.Log("-> Instagram «%s»: %s", q, err)
				continue
			}
			found := instagramSearchCandidates(page)
			for _, c := range found {
				c.Origin, c.OriginKind = "веб-поиск Instagram «"+q+"»", "other"
				out = append(out, c)
			}
			u.Log("-> Instagram «%s»: профилей найдено %d.", q, len(found))
		}
	}
	if id := u.NetworkAppKey("FACEBOOK_PAGE_ID"); metaDigits.MatchString(id) && u.NetworkAppKey("FACEBOOK_PAGE_ACCESS_TOKEN") != "" {
		body, err := u.metaGraphRead(id, url.Values{"fields": {"id,name,about"}}, "FACEBOOK_PAGE_ACCESS_TOKEN")
		if err != nil {
			u.Log("-> Facebook: %s", err)
		} else {
			info := jx.Unmarshal(body)
			for _, q := range queries {
				if QueryTextHit(strings.ToLower(jx.Str(info, "name")+" "+jx.Str(info, "about")), strings.ToLower(q)) {
					out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://www.facebook.com/" + id, Info: jx.M{"title": jx.Str(info, "name")}, Origin: "подключённая страница Facebook", OriginKind: "other"})
					break
				}
			}
		}
	} else {
		u.Log("-> Facebook: поиск пропущен, токен или ID страницы не настроен.")
	}
	return out
}
