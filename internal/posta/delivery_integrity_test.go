package posta

import (
	"os"
	"path/filepath"
	"testing"

	"posta/internal/jx"
)

func TestDeliveryPreflightRejectsLossAndMissingRequiredMedia(t *testing.T) {
	a := &App{}
	for _, tc := range []struct{ platform, text string; kinds []string }{
		{"vk", "caption", []string{"photo","audio"}},
		{"max", "caption", []string{"audio"}},
		{"youtube", "caption", nil},
		{"pinterest", "caption", nil},
	} {
		if err := a.NetworkDeliveryPreflight(tc.platform,tc.text,tc.kinds); err == nil {t.Fatalf("incompatible destination accepted: %+v",tc)}
	}
	if err := a.NetworkDeliveryPreflight("vk","caption",[]string{"image"}); err != nil {t.Fatal(err)}
	path:=filepath.Join(t.TempDir(),"photo.jpg")
	if err:=os.WriteFile(path,[]byte{1},0600);err!=nil{t.Fatal(err)}
	if err:=a.NetworkPreparedPreflight("vk","caption",[]Prepared{{Kind:"photo",Path:path}});err!=nil{t.Fatal(err)}
	if err:=a.NetworkPreparedPreflight("vk","caption",[]Prepared{{Kind:"photo",Path:path+".missing"}});err==nil{t.Fatal("missing prepared file accepted")}
}

func TestGalleryMainIdentityIncludesVideo(t *testing.T) {
	g:=[]jx.M{{"image":"cover","video":"one"},{"image":"cover","video":"two"},{"image":"cover"}}
	if galleryRecordMainIndex(g,jx.M{"image":"cover","video":"two"})!=1 || galleryRecordMainIndex(g,jx.M{"image":"cover"})!=2 {t.Fatal("shared cover confused distinct attachments")}
}
