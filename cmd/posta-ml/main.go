// posta-ml: CPU-only offline topic classifier training (research only).
package main

import (
 "bufio"
 "encoding/json"
 "errors"
 "flag"
 "fmt"
 "io"
 "os"
 "strings"

 "posta/internal/postaml"
)
const usage = `Usage:
  posta-ml train -data independently_reviewed.jsonl -model local-smm.json
  posta-ml predict -model local-smm.json -text "text of public post"

Example JSONL:
  {"source_id":"tg:example","text":"full independently reviewed post","label":"relevant","reviewed_by":"reviewer-id"}
Labels: irrelevant, adjacent, relevant. No network/GPU; never auto-approves sources.
`
func examples(filename string)([]postaml.Example,error){
 file,err:=os.Open(filename);if err!=nil{return nil,err};defer file.Close()
 scanner:=bufio.NewScanner(file);scanner.Buffer(make([]byte,64*1024),2*1024*1024)
 var rows []postaml.Example
 for line:=1;scanner.Scan();line++ {
  if strings.TrimSpace(scanner.Text())=="" {continue}
  var x postaml.Example
  if err:=json.Unmarshal(scanner.Bytes(),&x);err!=nil{return nil,fmt.Errorf("line %d: %w",line,err)}
  rows=append(rows,x)
 }
 if err:=scanner.Err();err!=nil{return nil,err}
 return rows,postaml.Validate(rows)
}
func modelFromFile(name string)(*postaml.Model,error){
 file,err:=os.Open(name);if err!=nil{return nil,err};defer file.Close()
 var m postaml.Model
 if err:=json.NewDecoder(io.LimitReader(file,20<<20)).Decode(&m);err!=nil{return nil,err}
 if err:=m.Validate();err!=nil{return nil,err}
 return &m,nil
}
func save(name string,object any) error {
 file,err:=os.OpenFile(name,os.O_CREATE|os.O_WRONLY|os.O_TRUNC,0600)
 if err!=nil{return err};defer file.Close()
 e:=json.NewEncoder(file);e.SetIndent("","  ");return e.Encode(object)
}
func run(args []string,stdout io.Writer) error {
 if len(args)==0{return errors.New(usage)}
 switch args[0] {
 case "train":
  fs:=flag.NewFlagSet("train",flag.ContinueOnError);fs.SetOutput(io.Discard)
  dataset:=fs.String("data","","independently reviewed dataset")
  output:=fs.String("model","","model file")
  epochs:=fs.Int("epochs",35,"training epochs")
  if err:=fs.Parse(args[1:]);err!=nil{return err}
  if *dataset==""||*output=="" {return errors.New("-data and -model required")}
  rows,err:=examples(*dataset);if err!=nil{return err}
  train,test,err:=postaml.SplitBySource(rows);if err!=nil{return err}
  model,err:=postaml.Train(train,*epochs);if err!=nil{return err}
  report,err:=postaml.Evaluate(model,test);if err!=nil{return err}
  if err:=save(*output,model);err!=nil{return err}
  return json.NewEncoder(stdout).Encode(struct {
   Status string `json:"status"`
   Heldout postaml.Report `json:"held_out_evaluation"`
   ModelFile string `json:"model_file"`
  }{"research_only_no_automatic_source_decisions",report,*output})
 case "predict":
  fs:=flag.NewFlagSet("predict",flag.ContinueOnError);fs.SetOutput(io.Discard)
  input:=fs.String("model","","trained model")
  text:=fs.String("text","","public post text")
  if err:=fs.Parse(args[1:]);err!=nil{return err}
  if *input==""||*text=="" {return errors.New("-model and -text required")}
  model,err:=modelFromFile(*input);if err!=nil{return err}
  prediction,err:=model.Predict(*text);if err!=nil{return err}
  return json.NewEncoder(stdout).Encode(prediction)
 default: return fmt.Errorf("unknown command %q\n%s",args[0],usage)
 }
}
func main(){
 if err:=run(os.Args[1:],os.Stdout);err!=nil{fmt.Fprintln(os.Stderr,err);os.Exit(1)}
}
