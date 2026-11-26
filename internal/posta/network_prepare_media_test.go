package posta

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestNetworkPhotoPreparationPreservesOriginal(t *testing.T){
	path:=filepath.Join(t.TempDir(),"photo")
	f,err:=os.Create(path);if err!=nil{t.Fatal(err)}
	if err=png.Encode(f,image.NewRGBA(image.Rect(0,0,20,30)));err!=nil{t.Fatal(err)}
	f.Close();st,err:=os.Stat(path);if err!=nil{t.Fatal(err)}
	a:=&App{}
	for _,k:=range []string{"max","ok","tumblr"}{
		p,ok:=networkOriginalPhoto(a.Networks()[k],path,"image/png",st.Size())
		if !ok||p.Path!=path||p.Width!=20||p.Height!=30{t.Fatalf("%s changed the original: %+v",k,p)}
	}
	if _,ok:=networkOriginalPhoto(a.Networks()["max"],path,"image/png",50_000_001);ok{t.Fatal("oversized file bypassed preparation")}
}
