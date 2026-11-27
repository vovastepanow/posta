package posta

import "testing"

func TestAutopilotSharedListingReusesOnlySuccessfulBoundedPublicRead(t *testing.T) {
	a := &App{autopilotListings: NewMemCache(2)}
	source := "telegram:marketing_test"
	res := FetchResultURL{FetchResult: FetchResult{OK:true, Status:200, Bytes:[]byte("original")}}
	calls := 0
	fetch := func(string) FetchResultURL { calls++; return res }
	if _, reused := a.autopilotReviewListing(source,source,true,fetch); reused || calls != 1 {
		t.Fatal("cold lookup should fetch")
	}
	a.rememberAutopilotListing(source,res,2)
	got, reused := a.autopilotReviewListing(source,source,true,fetch)
	if !reused || calls != 1 { t.Fatal("cross-topic listing was not reused") }
	got.Bytes[0] = 'X'
	got, _ = a.autopilotReviewListing(source,source,true,fetch)
	if string(got.Bytes)!="original" { t.Fatal("shared listing mutated") }
	if _, reused = a.autopilotReviewListing(source,source,false,fetch); reused || calls != 2 {
		t.Fatal("explicit recheck must fetch")
	}
	a.rememberAutopilotListing("telegram:empty_test",res,0)
	if _, reused = a.autopilotReviewListing("telegram:empty_test","",true,fetch); reused {
		t.Fatal("empty result cached")
	}
	a.rememberAutopilotListing("telegram:large_test",FetchResultURL{FetchResult:FetchResult{OK:true,Status:200,Bytes:make([]byte,autopilotListingMaxBytes+1)}},2)
	if _, reused = a.autopilotReviewListing("telegram:large_test","",true,fetch); reused {
		t.Fatal("oversized result cached")
	}
}
