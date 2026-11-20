package postaml

import (
 "fmt"
 "strings"
 "testing"
)

func synthetic() []Example {
 texts:=[3]string{
  "Юрист разбирает судебное решение, адвокатский договор и законы.",
  "Обзор контекстной рекламы, поисковых кампаний и общего брендинга.",
  "Практический SMM-кейс: вовлеченность подписчиков и рост охватов Telegram-канала.",
 }
 var rows []Example
 for class,label:=range Labels {
  for i:=0;i<10;i++ {
   rows=append(rows,Example{SourceID:fmt.Sprintf("%s-%d",label,i),
    Text:texts[class]+" Полезная статья о практике с конкретными подробностями.",Label:label,ReviewedBy:"independent"})
  }
 }
 return rows
}

func TestTrainAndHeldoutPublishers(t *testing.T){
 training,held,err:=SplitBySource(synthetic());if err!=nil{t.Fatal(err)}
 names:=map[string]bool{}
 for _,x:=range training{names[x.SourceID]=true}
 for _,x:=range held{if names[x.SourceID]{t.Fatal("publisher leaked into test")}}
 m,err:=Train(training,100);if err!=nil{t.Fatal(err)}
 report,err:=Evaluate(m,held);if err!=nil{t.Fatal(err)}
 if report.MacroF1<.75{t.Fatalf("simple synthetic signal not learned: %+v",report)}
 p,err:=m.Predict("Практический SMM-кейс: вовлеченность подписчиков и рост охватов Telegram-канала. Практика соцсетей.")
 if err!=nil||p.Label!="relevant"||!p.AdvisoryOnly{t.Fatalf("bad prediction %+v %v",p,err)}
}

func TestNeverTrainUnreviewedModelLabels(t *testing.T){
 data:=[]Example{{SourceID:"a",Text:strings.Repeat("Источник продвижение и аналитика. ",3),Label:"relevant"}}
 if _,err:=Train(data,20);err==nil{t.Fatal("unreviewed model label was accepted")}
}

func TestCorruptModelRejected(t *testing.T) {
 m:=NewModel();m.Weights[1]=nil
 if _,err:=m.Predict("Продвижение в социальных сетях.");err==nil{t.Fatal("invalid model accepted")}
}
