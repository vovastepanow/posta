package posta

import("testing";"posta/internal/jx")
func TestAdvancedPollSettings(t *testing.T){
	p:=jx.M{"question":"Q","options":[]string{"A","B"},"quiz":true,"correct_answer":"A","close_minutes":60}
	out,err:=NetworkPollCheck("telegram",p);if err!=nil||!jx.Bool(out,"quiz")||jx.Int(out,"close_minutes")!=60{t.Fatal(out,err)}
	if _,err=NetworkPollCheck("vk",p);err==nil{t.Fatal("VK quiz accepted")}
	p["correct_answer"]="C";if _,err=NetworkPollCheck("telegram",p);err==nil{t.Fatal("incorrect option accepted")}
	delete(p,"quiz");p["public"]=true
	if _,err=NetworkPollCheck("vk",p);err!=nil{t.Fatal(err)}
	if _,err=NetworkPollCheck("telegram",p);err==nil{t.Fatal("public Telegram poll accepted")}
}
