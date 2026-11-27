package posta

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/secret"
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
	for _,bad:=range []string{"http://example.org","https://user@example.org","https://example.org:8080","https://example.org/?q=1","https://example.org/#x","https://localhost/posta","https://127.0.0.1/posta","https://machine.local","https://example.org/?"} {
		if maxBotSiteURL(bad)!="" { t.Fatalf("accepted %q",bad) }
	}
	if maxBotSiteURL("https://example.org/posta/")!="https://example.org/posta" { t.Fatal("base path lost") }
}

func TestMaxServiceBotProfileLinkPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("requires disposable PostgreSQL")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil { t.Fatal(err) }
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil { t.Fatal(err) }
	a := &App{DB:d, Box:secret.FromKey([32]byte{1}), Cfg:config.Server{PublicURL:"http://localhost/posta"}}
	defer a.KVDelete(ctx,maxBotConfigKey)
	const credential = "test-token-not-a-real-credential"
	const botID int64 = 9007199254740993
	requests:=0
	err=a.maxBotConfigureWith(ctx,credential,"https://example.org/posta/",func(_ context.Context, method, raw string, headers map[string]string, body io.Reader)(*netResp,error){
		requests++
		if headers["Authorization"]!=credential { t.Fatal("wrong API credential") }
		switch raw {
		case maxAPI+"/me":
			if method!="GET" { t.Fatal("identity method") }
			return &netResp{Status:200,Raw:[]byte(`{"user_id":9007199254740993,"is_bot":true,"username":"posta_test_bot"}`)},nil
		case maxAPI+"/subscriptions":
			var payload struct {URL,Secret string; Types []string `json:"update_types"`}
			if method!="POST" || json.NewDecoder(body).Decode(&payload)!=nil || payload.URL!="https://example.org/posta/max-bot/webhook" || len(payload.Secret)!=64 || len(payload.Types)!=2 {
				t.Fatalf("bad subscription: %+v",payload)
			}
			if !a.MaxBotWebhookAuthorized(ctx,payload.Secret) || a.MaxBotWebhookAuthorized(ctx,"wrong") { t.Fatal("webhook secret not bound") }
			return &netResp{Status:200,Data:jx.M{"success":true}},nil
		default:
			t.Fatalf("unexpected endpoint %s",raw)
			return nil,nil
		}
	})
	if err!=nil || requests!=2 { t.Fatalf("configure: %v requests=%d",err,requests) }
	bot:=a.maxBotConfig(ctx)
	token,webhookSecret:=a.maxBotSecrets(bot)
	if !bot.Ready || bot.SiteURL!="https://example.org/posta" || a.maxBotSite(bot)!=bot.SiteURL || bot.ID!=botID || token!=credential || webhookSecret=="" || strings.Contains(string(a.KVRaw(ctx,maxBotConfigKey)),credential) { t.Fatal("invalid encrypted bot config") }
	uid,other:=randHex(16),randHex(16)
	for _, id:=range []string{uid,other}{
		if _,err:=d.Pool.Exec(ctx,`INSERT INTO users(id,created_at) VALUES($1,$2)`,id,now());err!=nil { t.Fatal(err) }
		defer d.Pool.Exec(ctx,`DELETE FROM users WHERE id=$1`,id)
	}
	state,err:=a.MaxBotLinkStart(ctx,uid)
	if err!=nil { t.Fatal(err) }
	linkToken:=jx.Str(state,"token")
	if !strings.HasSuffix(jx.Str(state,"url"),"?start=link_"+linkToken) { t.Fatal("bad link URL") }
	const maxID int64=9007199254740995
	if claimed,err:=a.maxBotClaimLink(ctx,bot.ID,maxID,linkToken);err!=nil || !claimed { t.Fatalf("claim: %v %v",claimed,err) }
	if claimed,err:=a.maxBotClaimLink(ctx,bot.ID,maxID+1,linkToken);err!=nil || claimed { t.Fatal("another MAX identity took claimed request") }
	code:=maxBotCode(webhookSecret,linkToken,maxID)
	if err=a.MaxBotLinkConfirm(ctx,other,linkToken,code);err==nil { t.Fatal("different browser profile confirmed link") }
	wrong:="00000000";if wrong==code { wrong="11111111" }
	if err=a.MaxBotLinkConfirm(ctx,uid,linkToken,wrong);err==nil { t.Fatal("incorrect code accepted") }
	if err=a.MaxBotLinkConfirm(ctx,uid,linkToken,code);err!=nil { t.Fatal(err) }
	if err=a.MaxBotLinkConfirm(ctx,uid,linkToken,code);err==nil { t.Fatal("confirmation replay accepted") }
	public,err:=a.MaxBotPublic(ctx,uid)
	if err!=nil || !jx.Bool(public,"configured") || jx.Str(public,"site_url")!="https://example.org/posta" || !jx.Bool(public,"linked") || strings.Contains(string(jx.Marshal(public)),credential) || strings.Contains(string(jx.Marshal(public)),webhookSecret) { t.Fatal("bad public state") }
	otherState,err:=a.MaxBotLinkStart(ctx,other)
	if err!=nil { t.Fatal(err) }
	otherToken:=jx.Str(otherState,"token")
	if claimed,err:=a.maxBotClaimLink(ctx,bot.ID,maxID,otherToken);err!=nil || !claimed { t.Fatalf("other claim %v %v",claimed,err) }
	if err=a.MaxBotLinkConfirm(ctx,other,otherToken,maxBotCode(webhookSecret,otherToken,maxID));err==nil { t.Fatal("MAX identity linked to two profiles") }
	if err=a.MaxBotUnlink(ctx,uid);err!=nil { t.Fatal(err) }
	public,err=a.MaxBotPublic(ctx,uid)
	if err!=nil || jx.Bool(public,"linked") { t.Fatal("unlink did not clear link") }
}
