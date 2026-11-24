package posta

import (
	"fmt"
	"testing"
	"time"
)

func TestAutopilotRutubeSearchCandidatesRequirePublicRecentVideos(t *testing.T) {
	at := time.Now()
	stamp := at.In(moscowZone).Format("2006-01-02T15:04:05")
	body := fmt.Sprintf(`{"results":[
		{"id":"111","title":"Astronomy tutorial","video_url":"https://rutube.ru/video/111/","created_ts":%q,"author":{"id":123,"name":"Astro"}},
		{"id":"222","title":"Another tutorial","video_url":"https://rutube.ru/video/222/","created_ts":%q,"author":{"id":123}},
		{"id":"333","title":"Hidden","video_url":"https://rutube.ru/video/333/","created_ts":%q,"is_hidden":true,"author":{"id":456}},
		{"id":"444","title":"Labeled","video_url":"https://rutube.ru/video/444/","created_ts":%q,"is_adult":true,"author":{"id":789}},
		{"id":"555","title":"Invalid origin","video_url":"https://evil.example/video/555/","created_ts":%q,"author":{"id":999}}
	]}`, stamp, stamp, stamp, stamp, stamp)
	r := FetchResultURL{URL: "https://rutube.ru/api/search/video/?query=astronomy", FetchResult: FetchResult{OK: true, Bytes: []byte(body)}}
	got, err := autopilotRutubeSearchCandidates(r, at.Unix())
	if err != nil || len(got) != 1 || got[0].ID != "https://rutube.ru/channel/123/" {
		t.Fatalf("public video source candidates: %+v %v", got, err)
	}
}
