package posta

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"posta/internal/jx"
)

// Follow explicit public CDN redirects and a publisher's alternate image
// representation. Never switch URLs on authorization or rate-limit errors.
// Each hop uses FetchPublic's DNS validation and the worker's fetch budget.
func autopilotReadPostImage(post jx.M, fetch func(string, FetchOpts) (FetchResult, error)) ([]byte, string, error) {
	primary, fallback := ContentImageKey(post), jx.Str(post,"image_fallback")
	seen := map[string]bool{}
	for _, raw := range []string{primary, fallback} {
		for hop := 0; hop <= 3; hop++ {
			if !autopilotPublicPostURL(raw,true) || seen[raw] {
				break
			}
			seen[raw] = true
			r,err:=fetch(raw,FetchOpts{MaxBytes:10<<20,Timeout:20*time.Second})
			if err!=nil {
				return nil,"",err
			}
			if r.Status==http.StatusUnauthorized || r.Status==http.StatusForbidden || r.Status==http.StatusTooManyRequests || r.Status==http.StatusServiceUnavailable {
				return nil,"",errors.New("public image access unavailable")
			}
			if r.Status>=300 && r.Status<400 && r.Location!="" {
				raw=RSSAbsoluteURL(raw,r.Location)
				continue
			}
			if r.OK && strings.HasPrefix(r.Type,"image/") && len(r.Bytes)>0 {
				return r.Bytes,raw,nil
			}
			break
		}
	}
	return nil,"",errors.New("public image unavailable")
}

func (u *U) autopilotReadPostImage(post jx.M) ([]byte,string,error) {
	return autopilotReadPostImage(post,u.FetchPublic)
}
