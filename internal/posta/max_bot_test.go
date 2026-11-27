package posta

import (
	"encoding/json"
	"testing"
)

func TestMaxBotPrivateCommands(t *testing.T) {
	for _,tc:=range []struct{body,command string;id int64}{
		{`{"update_type":"bot_started","user":{"user_id":9007199254740993},"payload":"link_test"}`,"/start link_test",9007199254740993},
		{`{"update_type":"message_created","message":{"sender":{"user_id":12},"recipient":{"chat_type":"dialog"},"body":{"text":"/status"}}}`,"/status",12},
		{`{"update_type":"message_created","message":{"sender":{"user_id":12},"recipient":{"chat_type":"chat"},"body":{"text":"/status"}}}`,"",0},
		{`{"update_type":"bot_started","user":{"user_id":12,"is_bot":true}}`,"",0},
		{`{"update_type":"message_created","message":{"sender":{"user_id":12,"is_bot":true},"recipient":{"chat_type":"dialog"},"body":{"text":"/status"}}}`,"",0},
	}{
		var upd maxBotUpdate
		if err:=json.Unmarshal([]byte(tc.body),&upd);err!=nil { t.Fatal(err) }
		id,command:=maxBotPrivateCommand(upd)
		if id!=tc.id || command!=tc.command { t.Fatalf("id=%d command=%q",id,command) }
	}
}
func TestMaxBotCodeAndHTTPS(t *testing.T){
	a:=maxBotCode("secret","token",123)
	if !maxBotConfirmCode.MatchString(a) || a!=maxBotCode("secret","token",123) || a==maxBotCode("secret","token",124) || a==maxBotCode("other","token",123) { t.Fatal("confirmation not bound to identity and secret") }
	for _,bad:=range []string{"http://example.org","https://user@example.org","https://example.org:8080","https://example.org/?q=1","https://example.org/#x"} {
		if maxBotSiteURL(bad)!="" { t.Fatalf("accepted %q",bad) }
	}
	if maxBotSiteURL("https://example.org/posta/")!="https://example.org/posta" { t.Fatal("base path lost") }
}
