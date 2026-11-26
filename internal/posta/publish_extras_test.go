package posta

import (
	"testing"
	"posta/internal/jx"
)

func TestPublishExtrasValidation(t *testing.T){
	buttons:=jx.M{"buttons":[]jx.M{{"text":"Open","url":"https://example.com"}}}
	for _,platform:=range []string{"telegram","max"}{
		out,err:=PublishExtrasCheck(platform,"text",nil,nil,buttons)
		if err!=nil||len(jx.Maps(out,"buttons"))!=1{t.Fatal(out,err)}
	}
	for _,tc:=range []struct{platform string;kinds []string;extra jx.M}{
		{"telegram",[]string{"photo","video"},buttons},
		{"vk",nil,buttons},
		{"telegram",[]string{"audio"},jx.M{"spoiler":true}},
		{"max",nil,jx.M{"silent":true}},
		{"telegram",nil,jx.M{"buttons":[]jx.M{{"text":"x","url":"javascript:alert(1)"}}}},
	}{
		if _,err:=PublishExtrasCheck(tc.platform,"text",tc.kinds,nil,tc.extra);err==nil{t.Fatal("accepted",tc)}
	}
	fields:=map[string]string{}
	telegramExtraFields(fields,jx.Merge(buttons,jx.M{"silent":true,"protect":true}))
	if fields["disable_notification"]!="true"||fields["protect_content"]!="true"||fields["reply_markup"]==""{t.Fatal(fields)}
}

func TestPlatformSpecificExtras(t *testing.T){
 v,err:=PublishExtrasCheck("vk","post",nil,nil,jx.M{"signed":true,"close_comments":true,"mark_as_ads":true,"copyright":"https://example.com","link_attachment":"https://example.org","vk_geo":"55.75,37.62"})
 if err!=nil||!jx.Bool(v,"signed")||jx.Str(v,"vk_geo")!="55.75,37.62"{t.Fatal(v,err)}
 for _,tc:=range []struct{platform string;kinds []string;extra jx.M}{
 {"telegram",nil,jx.M{"signed":true}},
 {"threads",[]string{"photo"},jx.M{"link_attachment":"https://example.org"}},
 {"instagram",[]string{"video"},jx.M{"alt_text":"alt"}},
 {"vk",nil,jx.M{"vk_geo":"NaN,2"}},
 }{if _,err:=PublishExtrasCheck(tc.platform,"post",tc.kinds,nil,tc.extra);err==nil{t.Fatal(tc)}}
 if _,err:=PublishExtrasCheck("threads","post",[]string{"photo"},nil,jx.M{"alt_text":"alt","spoiler":true,"topic_tag":"Design"});err!=nil{t.Fatal(err)}
}
