// Package postaml provides an experimental CPU-only topic classifier.
// Predictions are advisory: never approve or reject a source using this alone.
package postaml

import (
 "errors"
 "fmt"
 "hash/fnv"
 "math"
 "sort"
 "strings"
 "unicode"
)

const Dimension = 1 << 15
var Labels = [3]string{"irrelevant","adjacent","relevant"}

// Example must be independently reviewed; existing Nemotron labels are not gold data.
type Example struct {
 SourceID string `json:"source_id"`
 Text string `json:"text"`
 Label string `json:"label"`
 ReviewedBy string `json:"reviewed_by"`
}

type Model struct {
 Version int `json:"version"`
 Labels [3]string `json:"labels"`
 Weights [3][]float64 `json:"weights"`
 Bias [3]float64 `json:"bias"`
 TrainingPosts int `json:"training_posts"`
}
type Prediction struct {
 Label string `json:"label"`
 Confidence float64 `json:"confidence"`
 Probabilities map[string]float64 `json:"probabilities"`
 AdvisoryOnly bool `json:"advisory_only"`
}
type pair struct { index int; value float64 }
func labelIndex(name string) int {
 for i,v:=range Labels {if name==v{return i}}
 return -1
}
func Validate(examples []Example) error {
 if len(examples)==0{return errors.New("no independently reviewed examples")}
 for i,x:=range examples {
  if x.SourceID=="" || x.ReviewedBy=="" || labelIndex(x.Label)<0 || len([]rune(strings.TrimSpace(x.Text)))<25 {
   return fmt.Errorf("example %d requires source_id, reviewed_by, 25+ text runes and relevant/adjacent/irrelevant label", i+1)
  }
 }
 return nil
}
func hashed(s string) int {
 h:=fnv.New64a();_,_=h.Write([]byte(s))
 return int(h.Sum64()%Dimension)
}
func extract(text string) []pair {
 r:=[]rune(strings.ToLower(strings.TrimSpace(text)))
 if len(r)>6000 {r=append(append([]rune{},r[:4200]...),r[len(r)-1800:]...)}
 vals:=make(map[int]float64)
 for size:=3;size<=5;size++ {
  for i:=0;i+size<=len(r);i++ {vals[hashed("c"+string(r[i:i+size]))]+=.35}
 }
 var words []string
 start:=-1
 for i,c:=range r {
  if unicode.IsLetter(c)||unicode.IsDigit(c) {if start<0 {start=i}} else if start>=0 {words=append(words,string(r[start:i]));start=-1}
 }
 if start>=0 {words=append(words,string(r[start:]))}
 for i,w:=range words {
  if len([]rune(w))>=2 {vals[hashed("w"+w)]+=2}
  if i>0 {vals[hashed("b"+words[i-1]+"_"+w)]+=1.2}
 }
 out:=make([]pair,0,len(vals));norm:=0.0
 for index,value:=range vals {value=math.Log1p(value);out=append(out,pair{index,value});norm+=value*value}
 sort.Slice(out,func(i,j int)bool{return out[i].index<out[j].index})
 if norm>0 {norm=math.Sqrt(norm);for i:=range out {out[i].value/=norm}}
 return out
}
func NewModel() *Model {
 m:=&Model{Version:1,Labels:Labels}
 for i:=range m.Weights {m.Weights[i]=make([]float64,Dimension)}
 return m
}
func (m *Model) Validate() error {
 if m.Version!=1||m.Labels!=Labels{return errors.New("incompatible model")}
 for _,w:=range m.Weights {if len(w)!=Dimension{return errors.New("missing or corrupt model weights")}}
 return nil
}
func (m *Model) probabilities(x []pair) [3]float64 {
 var scores [3]float64;peak:=math.Inf(-1)
 for i:=0;i<3;i++ {scores[i]=m.Bias[i];for _,v:=range x {scores[i]+=m.Weights[i][v.index]*v.value};if scores[i]>peak {peak=scores[i]}}
 total:=0.0
 for i:=range scores {scores[i]=math.Exp(scores[i]-peak);total+=scores[i]}
 for i:=range scores {scores[i]/=total}
 return scores
}
func Train(data []Example,epochs int)(*Model,error){
 if err:=Validate(data);err!=nil{return nil,err}
 if epochs<1||epochs>150{return nil,errors.New("epochs must be 1..150")}
 type sample struct {label int;f []pair}
 rows:=make([]sample,0,len(data))
 for _,x:=range data {rows=append(rows,sample{labelIndex(x.Label),extract(x.Text)})}
 m:=NewModel();step:=0
 for epoch:=0;epoch<epochs;epoch++ {
  for _,row:=range rows {
   step++;lr:=.75/math.Sqrt(1+float64(step)/250)
   p:=m.probabilities(row.f)
   for class:=0;class<3;class++ {
    y:=0.0;if class==row.label {y=1}
    g:=y-p[class];m.Bias[class]+=lr*g*.12
    for _,v:=range row.f {
     w:=&m.Weights[class][v.index]
     *w+=lr*(g*v.value-.0002**w)
    }
   }
  }
 }
 m.TrainingPosts=len(data);return m,nil
}
func (m *Model) Predict(text string)(Prediction,error){
 if err:=m.Validate();err!=nil{return Prediction{},err}
 if strings.TrimSpace(text)==""{return Prediction{},errors.New("empty text")}
 p:=m.probabilities(extract(text));best:=0
 for i:=1;i<3;i++ {if p[i]>p[best]{best=i}}
 result:=Prediction{Label:Labels[best],Confidence:p[best],AdvisoryOnly:true,Probabilities:map[string]float64{}}
 for i,name:=range Labels {result.Probabilities[name]=p[i]}
 return result,nil
}
// SplitBySource holds out complete publishers, preventing train/test leakage.
func SplitBySource(data []Example)(train,test []Example,err error){
 if err=Validate(data);err!=nil{return nil,nil,err}
 groups:=map[string][]Example{}
 for _,x:=range data {groups[x.SourceID]=append(groups[x.SourceID],x)}
 var byClass [3][]string
 for id,rows:=range groups {
  counts:=[3]int{}
  for _,x:=range rows {counts[labelIndex(x.Label)]++}
  major:=0;for c:=1;c<3;c++ {if counts[c]>counts[major]{major=c}}
  byClass[major]=append(byClass[major],id)
 }
 held:=map[string]bool{}
 for c,ids:=range byClass {
  if len(ids)<5{return nil,nil,fmt.Errorf("need 5+ independently reviewed sources in %s, got %d",Labels[c],len(ids))}
  sort.Strings(ids)
  for i,id:=range ids {if i%5==0{held[id]=true}}
 }
 for _,x:=range data {if held[x.SourceID]{test=append(test,x)}else{train=append(train,x)}}
 return train,test,nil
}
type Report struct {
 TrainingPosts int `json:"training_posts"`
 HeldOutPosts int `json:"held_out_posts"`
 HeldOutSources int `json:"held_out_sources"`
 Confusion [3][3]int `json:"confusion_true_rows_predicted_columns"`
 Accuracy float64 `json:"accuracy"`
 MacroF1 float64 `json:"macro_f1"`
 RelevantPrecision float64 `json:"relevant_precision"`
 RelevantRecall float64 `json:"relevant_recall"`
}
func Evaluate(model *Model,data []Example)(Report,error){
 result:=Report{}
 if err:=Validate(data);err!=nil{return result,err}
 if err:=model.Validate();err!=nil{return result,err}
 seen:=map[string]bool{}
 for _,x:=range data {
  p,err:=model.Predict(x.Text);if err!=nil{return result,err}
  result.Confusion[labelIndex(x.Label)][labelIndex(p.Label)]++
  seen[x.SourceID]=true
 }
 result.TrainingPosts=model.TrainingPosts;result.HeldOutPosts=len(data);result.HeldOutSources=len(seen)
 correct:=0;macro:=0.0
 for c:=0;c<3;c++ {
  correct+=result.Confusion[c][c]
  tp,actual,predicted:=result.Confusion[c][c],0,0
  for j:=0;j<3;j++ {actual+=result.Confusion[c][j];predicted+=result.Confusion[j][c]}
  precision,recall:=0.0,0.0
  if predicted>0{precision=float64(tp)/float64(predicted)}
  if actual>0{recall=float64(tp)/float64(actual)}
  if precision+recall>0{macro+=2*precision*recall/(precision+recall)}
  if c==2{result.RelevantPrecision=precision;result.RelevantRecall=recall}
 }
 result.Accuracy=float64(correct)/float64(len(data));result.MacroF1=macro/3
 return result,nil
}
