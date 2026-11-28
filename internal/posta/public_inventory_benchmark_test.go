package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestInventoryBenchmarkScore(t *testing.T) {
	yes,no:=true,false
	r:=InventoryBenchmarkRanks{Baseline:[]string{"a","b"},Open:[]string{"a","c","d"},Control:[]string{"e"}}
	scores,err:=ScoreInventoryBenchmark(r,[]InventoryBenchmarkLabel{{"a",&yes},{"b",&no},{"c",&yes},{"d",nil}})
	if err!=nil{t.Fatal(err)}
	base,open:=scores["baseline"][10],scores["open"][10]
	if base.Judged!=2 || *base.Precision!=0.5 || *base.PooledRecall!=0.5 ||
		open.Returned!=3 || open.Judged!=2 || *open.Precision!=1 || *open.PooledRecall!=1 {
		t.Fatalf("unexpected scores: %+v %+v",base,open)
	}
	if scores["control"][10].Precision!=nil{t.Fatal("unjudged sample became a negative label")}
	if _,err=ScoreInventoryBenchmark(r,[]InventoryBenchmarkLabel{{"z",&yes}});err==nil{t.Fatal("unknown id accepted")}
	if _,err=ScoreInventoryBenchmark(r,[]InventoryBenchmarkLabel{{"a",&yes},{"a",&no}});err==nil{t.Fatal("duplicate id accepted")}
}

func TestInventoryBenchmarkPostgres(t *testing.T) {
	dsn:=os.Getenv("POSTA_TEST_PG")
	if dsn==""{t.Skip("POSTA_TEST_PG not set")}
	ctx:=context.Background()
	d,err:=db.Open(ctx,dsn)
	if err!=nil{t.Fatal(err)}
	defer d.Close()
	if _,err=d.Migrate(ctx);err!=nil{t.Fatal(err)}
	a:=&App{DB:d}
	if err=a.LoadCatalog(ctx);err!=nil{t.Fatal(err)}
	allowed:=a.publicEmbeddingSources()
	if len(allowed)==0{t.Fatal("test catalog lacks public sources")}
	source:=allowed[0]
	suffix:=randHex(8)
	post:=jx.M{"url":"https://example.org/benchmark/"+suffix,"text":"интеграционный "+suffix+" маркетинг"}
	key:=CandidatePostKey(post)
	defer d.Pool.Exec(ctx,`DELETE FROM public_posts WHERE source_key=$1 AND post_key=$2`,source,key)
	a.storePublicPosts(ctx,PlatformRSS,source,[]jx.M{post})
	if _,err=d.Pool.Exec(ctx,`INSERT INTO public_post_topics(source_key,post_key,topic_key) VALUES($1,$2,$3)`,source,key,"benchmark-"+suffix);err!=nil{t.Fatal(err)}
	r,blind,err:=a.BuildInventoryBenchmark(ctx,suffix,"benchmark-"+suffix,nil)
	if err!=nil{t.Fatal(err)}
	want:=repeatDigest(key)
	has:=func(ids []string)bool{for _,id:=range ids{if id==want{return true}};return false}
	health:=a.publicInventoryStatus(ctx)
	if !jx.Bool(health,"available") || jx.Int(health,"eligible_posts")<1 || jx.Int(health,"eligible_sources")<1 {
		t.Fatalf("inventory status missing eligible records: %v",health)
	}
	if !has(r.Baseline)||!has(r.Open){t.Fatalf("fixture missing from one retrieval path: %+v",r)}
	for _,sample:=range blind{if sample.ID==want{return}}
	t.Fatal("ranked post missing from blind sample")
}
