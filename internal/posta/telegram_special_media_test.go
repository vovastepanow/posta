package posta
import("testing";"posta/internal/jx")
func TestTelegramSpecialMedia(t *testing.T){
 good:=VideoInfo{Container:"mp4",Video:"h264",Audio:"aac",Width:480,Height:480,Duration:60}
 if err:=telegramVideoNoteCheck(good,10000);err!=nil{t.Fatal(err)}
 good.Height=320;if telegramVideoNoteCheck(good,10000)==nil{t.Fatal("rectangular note accepted")}
 if TelegramMediaTextCheck("caption",[]string{"sticker"})==nil||TelegramMediaTextCheck("",[]string{"photo","video_note"})==nil{t.Fatal("invalid special media accepted")}
 if TelegramMediaTextCheck("",[]string{"video_note"})!=nil{t.Fatal("valid video note rejected")}
 ex,err:=PublishExtrasCheck("max","",nil,nil,jx.M{"max_sticker":"2fd04f94e"})
 if err!=nil||!PostExtrasStandalone(ex)||jx.Str(jx.Map(maxStandaloneAttachment(ex),"payload"),"code")!="2fd04f94e"{t.Fatal(ex,err)}
}
