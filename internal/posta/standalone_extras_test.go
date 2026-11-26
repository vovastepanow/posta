package posta

import("testing";"strings";"posta/internal/jx")

func TestStandalonePostExtras(t *testing.T){
	location:=jx.M{"location":jx.M{"latitude":"0","longitude":"24.94"}}
	for _,platform:=range []string{"telegram","max"}{
		extra,err:=PublishExtrasCheck(platform,"",nil,nil,location)
		if err!=nil||!PostExtrasStandalone(extra){t.Fatal(extra,err)}
		method,fields:=telegramStandaloneFields(extra)
		if method!="sendLocation"||fields["latitude"]!="0"{t.Fatal(method,fields)}
		if jx.Str(maxStandaloneAttachment(extra),"type")!="location"{t.Fatal(extra)}
	}
	if _,err:=PublishExtrasCheck("vk","",nil,nil,location);err==nil{t.Fatal("unsupported accepted")}
	if _,err:=PublishExtrasCheck("telegram","text",nil,nil,location);err==nil{t.Fatal("combined accepted")}
	if _,err:=PublishExtrasCheck("telegram","",nil,nil,jx.M{"location":jx.M{"latitude":"NaN","longitude":"0"}});err==nil{t.Fatal("NaN accepted")}
	extra,err:=PublishExtrasCheck("max","",nil,nil,jx.M{"contact":jx.M{"first_name":"A;B","phone":"+3581234567"}})
	if err!=nil{t.Fatal(err)}
	card:=jx.Str(jx.Map(maxStandaloneAttachment(extra),"payload"),"vcf_info")
	if !strings.Contains(card,"A\\;B"){t.Fatal(card)}
}
