package posta

import "strings"

func telegramVideoNoteCheck(probe VideoInfo,size int64)error{
 if !telegramVideoReady(probe)||probe.Width<=0||probe.Width!=probe.Height||probe.Width>640||probe.Duration<=0||probe.Duration>60||size<=0||size>50_000_000{
  return Bad("Видеокружок: квадратный MP4/H.264 до 640×640, до 60 секунд и 50 МБ. Обрезка и перекодирование не выполняются автоматически.")
 }
 return nil
}
func TelegramStandaloneMediaCheck(text string,kinds []string)error{
 for _,k:=range kinds{
  if k=="sticker"||k=="video_note"{
   if len(kinds)!=1||strings.TrimSpace(text)!=""{return Bad("Стикер и видеокружок не имеют подписи и отправляются отдельно.")}
  }
 }
 return nil
}
func TelegramMediaTextCheck(text string,kinds []string)error{
 if err:=TelegramStandaloneMediaCheck(text,kinds);err!=nil{return err}
 return TelegramCheckText(text,len(kinds))
}
