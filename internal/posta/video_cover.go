package posta
import("fmt";"os";"path/filepath";"net/http";"posta/internal/imgx";"posta/internal/jx")
func SupportsVideoCover(platform string)bool{return contains([]string{"","telegram","instagram","pinterest","youtube"},platform)}
func prepareVideoCover(path,dir string,index int)(string,error){
 info,err:=os.Stat(path);if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>10_000_000{return "",Bad("Обложка: изображение до 10 МБ.")}
 b,err:=os.ReadFile(path);if err!=nil{return "",err}
 if !contains([]string{"image/jpeg","image/png","image/webp"},http.DetectContentType(b)){return "",Bad("Обложка: JPEG, PNG или WEBP.")}
 w,h,ok:=imgx.Size(b);if !ok||w<64||h<64||int64(w)*int64(h)>40_000_000||float64(max(w,h))/float64(min(w,h))>20{return "",Bad("Некорректный размер обложки.")}
 src,err:=imgx.Decode(b);if err!=nil{return "",Bad("Не удалось прочитать изображение обложки.")}
 dst:=filepath.Join(dir,fmt.Sprintf("cover-%d.jpg",index))
 if http.DetectContentType(b)=="image/jpeg"&&len(b)<=2_000_000&&max(w,h)<=1920{if err=os.WriteFile(dst,b,0600);err!=nil{return "",err};return dst,nil}
 resized:=imgx.ResizeImage(src,1920)
 for q:=90;q>=60;q-=10{data:=imgx.EncodeJPEG(resized,q);if len(data)==0||len(data)>2_000_000||http.DetectContentType(data)!="image/jpeg"{continue};if err=os.WriteFile(dst,data,0600);err!=nil{return "",err};return dst,nil}
 return "",Bad("Не удалось подготовить JPEG-обложку.")
}
func CheckVideoCovers(platform string,media []Prepared,extras jx.M)error{
 for _,f:=range media{if f.CoverPath==""{continue};if !SupportsVideoCover(platform)||f.Kind!="video"||((platform!=""&&platform!="telegram")&&len(media)!=1)||jx.Bool(extras,"ig_story")||jx.Str(extras,"ig_cover_seconds")!=""{return Bad("Обложка несовместима с форматом публикации.")}
 info,err:=os.Stat(f.CoverPath);if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>2_000_000||sniffMime(f.CoverPath)!="image/jpeg"{return Bad("Подготовленная обложка отсутствует или повреждена.")}
 }
 return nil
}
