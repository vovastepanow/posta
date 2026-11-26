package posta

import (
 "crypto/rand"
 "encoding/hex"
 "fmt"
 "io"
 "net/http"
 "net/url"
 "os"
 "regexp"
 "strings"
 "sync"
 "time"
)

const deliveryMediaTTL = 15 * time.Minute
const deliveryMediaLimit = 100

type deliveryMedia struct {
 app *App
 file *os.File
 size int64
 mime, name string
 expires time.Time
}

var deliveryMediaLeases = struct {
 sync.Mutex
 entries map[string]*deliveryMedia
}{entries: make(map[string]*deliveryMedia)}
var deliveryMediaKey = regexp.MustCompile("^[a-f0-9]{64}$")

// leaseDeliveryMedia grants temporary bearer access to one already prepared
// file, solely for a publishing provider that pulls media via HTTPS.
func (a *App) leaseDeliveryMedia(p Prepared) (string, func(), error) {
 base, err := url.Parse(a.Cfg.PublicURL)
 if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" || (base.Port() != "" && base.Port() != "443") {
  return "", nil, fmt.Errorf("для передачи медиа настройте публичный HTTPS-адрес Posta")
 }
 f, err := os.Open(p.Path)
 if err != nil { return "",nil,err }
 st,err:=f.Stat()
 if err!=nil || !st.Mode().IsRegular() || st.Size()<=0 { f.Close(); return "",nil,fmt.Errorf("недоступный медиафайл") }
 var buf [512]byte
 n,_:=f.ReadAt(buf[:],0)
 mime:=http.DetectContentType(buf[:n])
 name:=""
 switch mime {
 case "image/jpeg": name="media.jpg"
 case "image/png": name="media.png"
 case "video/mp4": name="media.mp4"
 default: f.Close();return "",nil,fmt.Errorf("формат передачи медиа не поддерживается: %s",mime)
 }
 if (p.Kind=="photo" && !strings.HasPrefix(mime,"image/")) || (p.Kind=="video" && mime!="video/mp4") || (p.Kind!="photo" && p.Kind!="video") { f.Close();return "",nil,fmt.Errorf("тип вложения не соответствует содержимому") }
 var raw [32]byte
 if _,err=rand.Read(raw[:]);err!=nil{f.Close();return "",nil,err}
 key:=hex.EncodeToString(raw[:])
 lease:=&deliveryMedia{app:a,file:f,size:st.Size(),mime:mime,name:name,expires:time.Now().Add(deliveryMediaTTL)}
 deliveryMediaLeases.Lock()
 if len(deliveryMediaLeases.entries)>=deliveryMediaLimit {deliveryMediaLeases.Unlock();f.Close();return "",nil,fmt.Errorf("лимит временных ссылок; повторите позже")}
 deliveryMediaLeases.entries[key]=lease
 deliveryMediaLeases.Unlock()
 var once sync.Once
 revoke:=func(){once.Do(func(){deliveryMediaLeases.Lock();delete(deliveryMediaLeases.entries,key);deliveryMediaLeases.Unlock();f.Close()})}
 timer:=time.AfterFunc(deliveryMediaTTL,revoke)
 return strings.TrimRight(base.String(),"/")+"/delivery-media/"+key+"/"+name,func(){timer.Stop();revoke()},nil
}

func (a *App) ServeDeliveryMedia(w http.ResponseWriter,r *http.Request){
 w.Header().Set("Cache-Control","private, no-store")
 w.Header().Set("Referrer-Policy","no-referrer")
 w.Header().Set("X-Robots-Tag","noindex, nofollow")
 w.Header().Set("X-Content-Type-Options","nosniff")
 if r.Method!=http.MethodGet && r.Method!=http.MethodHead {w.Header().Set("Allow","GET, HEAD");w.WriteHeader(http.StatusMethodNotAllowed);return}
 parts:=strings.Split(strings.TrimPrefix(r.URL.Path,"/delivery-media/"),"/")
 if len(parts)!=2||!deliveryMediaKey.MatchString(parts[0]){http.NotFound(w,r);return}
 deliveryMediaLeases.Lock()
 l:=deliveryMediaLeases.entries[parts[0]]
 deliveryMediaLeases.Unlock()
 if l==nil||l.app!=a||l.name!=parts[1]||!time.Now().Before(l.expires){http.NotFound(w,r);return}
 w.Header().Set("Content-Type",l.mime)
 http.ServeContent(w,r,l.name,time.Time{},io.NewSectionReader(l.file,0,l.size))
}
