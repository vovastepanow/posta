package main

import (
 "bytes"
 "os"
 "path/filepath"
 "strings"
 "testing"
)
func TestNoUnreviewedTraining(t *testing.T) {
 dir:=t.TempDir()
 input:=filepath.Join(dir,"data.jsonl")
 text:=`{"source_id":"tg:example","text":"Длинный пост о социальной рекламе и вовлеченности аудитории","label":"relevant"}`+"\n"
 if err:=os.WriteFile(input,[]byte(text),0600);err!=nil{t.Fatal(err)}
 var out bytes.Buffer
 err:=run([]string{"train","-data",input,"-model",filepath.Join(dir,"model.json")},&out)
 if err==nil||!strings.Contains(err.Error(),"reviewed_by"){t.Fatalf("unreviewed labels accepted: %v",err)}
}
func TestModelMustExist(t *testing.T){
 var out bytes.Buffer
 if err:=run([]string{"predict","-model","/does-not-exist","-text","hello"},&out);err==nil{
  t.Fatal("nonexistent model accepted")
 }
}
