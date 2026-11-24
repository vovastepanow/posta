package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAutopilotMediaReaderRedirectAndFallback(t *testing.T) {
	post := jx.M{"image":"https://example.org/image.jpg","image_fallback":"https://example.org/preview.jpg"}
	calls:=0
	body,raw,err:=autopilotReadPostImage(post,func(url string, opts FetchOpts)(FetchResult,error){
		calls++
		switch calls {
		case 1:
			if url!="https://example.org/image.jpg" || opts.MaxBytes!=10<<20 {
				t.Fatal("unbounded or incorrect fetch")
			}
			return FetchResult{Status:302,Location:"/moved.jpg"},nil
		case 2:
			if url!="https://example.org/moved.jpg" {
				t.Fatal("wrong redirect")
			}
			return FetchResult{Status:404},nil
		case 3:
			if url!="https://example.org/preview.jpg" {
				t.Fatal("wrong fallback")
			}
			return FetchResult{OK:true,Type:"image/jpeg",Bytes:[]byte("image")},nil
		}
		t.Fatal("unexpected fetch")
		return FetchResult{},nil
	})
	if err!=nil || string(body)!="image" || raw!="https://example.org/preview.jpg" || calls!=3 {
		t.Fatalf("media recovery failed: %q %q %d %v",body,raw,calls,err)
	}
	calls=0
	_,_,err=autopilotReadPostImage(post,func(string,FetchOpts)(FetchResult,error){
		calls++
		return FetchResult{Status:403},nil
	})
	if err==nil || calls!=1 {
		t.Fatalf("access restriction bypassed: calls=%d err=%v",calls,err)
	}
}
