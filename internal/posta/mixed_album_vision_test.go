package posta

import (
	"bytes"
	"image"
	"image/png"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestMixedAlbumSamplesEveryMemberInOrder(t *testing.T) {
	var b bytes.Buffer
	if err := png.Encode(&b,image.NewRGBA(image.Rect(0,0,256,256))); err != nil { t.Fatal(err) }
	g := []jx.M{{"image":"one"},{"video":"two"},{"video":"three"}}
	var calls []string
	frames, counts, ok := mixedAlbumVisionFrames(g,func(f jx.M)[]byte{calls=append(calls,jx.Str(f,"image"));return b.Bytes()},func(f jx.M)[][]byte{calls=append(calls,jx.Str(f,"video"));return [][]byte{b.Bytes(),b.Bytes()}})
	if !ok || len(frames)!=5 || strings.Join(calls,",")!="one,two,three" || len(counts)!=3 || counts[1]!=2 {t.Fatalf("incomplete album evidence: %v %v %v",calls,counts,ok)}
	item := jx.M{"gallery":g,"_album_frame_counts":[]any{1,2,2}}
	if visualEvidenceKind(item,5,nil)!="mixed_album" || !strings.Contains(visualEvidenceDescription(item,5,nil),"вложение 3") {t.Fatal("mixed album treated as one video")}
	_,_,ok=mixedAlbumVisionFrames(g,func(jx.M)[]byte{return b.Bytes()},func(jx.M)[][]byte{return nil})
	if ok {t.Fatal("unavailable video treated as inspected")}
}

func TestMixedAlbumClassificationRequiresAllMembers(t *testing.T) {
	g:=[]jx.M{{"image":"one"},{"video":"two"},{"video":"three"}}
	n:=0
	tags, unsafe := classifyMixedAlbumMembers(g,func(jx.M)([]string,bool){n++;if n==2{return nil,false};return []string{"tag"},false})
	if tags!=nil || unsafe || n!=3 {t.Fatalf("partial classification accepted: %v %v %d",tags,unsafe,n)}
	n=0
	tags,unsafe=classifyMixedAlbumMembers(g,func(jx.M)([]string,bool){n++;return []string{"tag"},n==3})
	if tags!=nil || !unsafe {t.Fatal("unsafe later member ignored")}
}
