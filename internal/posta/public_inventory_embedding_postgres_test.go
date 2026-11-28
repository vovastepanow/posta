package posta

import (
	"context"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
)

func TestPublicEmbeddingRetrievalPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &App{DB:d}
	source := "integration-embedding-"+randHex(8)
	blocked := source+"-blocked"
	defer d.Pool.Exec(ctx,"DELETE FROM public_posts WHERE source_key=ANY($1::text[])",[]string{source,blocked})
	model := "test-512-v1"
	v := func(x,y float32) []float32 {
		out := make([]float32,512)
		out[0],out[1]=x,y
		return out
	}
	post := func(id,text string) jx.M {
		return jx.M{"url":"https://example.com/embedding/"+id,"text":text}
	}
	first := post("first","Social media marketing")
	second := post("second","Crochet toys")
	other := post("blocked","Social networks")
	sensitive := post("sensitive","Advertising campaign")
	sensitive["source_sensitive"] = true
	a.storePublicPosts(ctx,PlatformRSS,source,[]jx.M{first,second,sensitive})
	a.storePublicPosts(ctx,PlatformRSS,blocked,[]jx.M{other})
	save := func(source, model string, p jx.M, vector []float32) int64 {
		t.Helper()
		tag,err:=d.Pool.Exec(ctx,publicEmbeddingSaveSQL,source,CandidatePostKey(p),jx.Marshal(p),model,vector,now())
		if err!=nil { t.Fatal(err) }
		return tag.RowsAffected()
	}
	save(source,model,first,v(1,0))
	save(source,model,second,v(0,1))
	save(source,model,sensitive,v(1,0))
	save(blocked,model,other,v(1,0))
	save(source,"wrong-version",first,v(0,1))
	find := func(sql string) []string {
		t.Helper()
		rows,err:=d.Pool.Query(ctx,sql,model,[]string{source},v(1,0),120)
		if err!=nil { t.Fatal(err) }
		defer rows.Close()
		var result []string
		for rows.Next() {
			var platform string
			var raw []byte
			if err:=rows.Scan(&platform,&raw);err!=nil { t.Fatal(err) }
			result=append(result,jx.Str(jx.Unmarshal(raw),"url"))
		}
		if err:=rows.Err();err!=nil { t.Fatal(err) }
		return result
	}
	assertOrder := func(sql string) {
		t.Helper()
		got:=find(sql)
		if len(got)!=2 || got[0]!=jx.Str(first,"url") || got[1]!=jx.Str(second,"url") {
			t.Fatalf("unexpected vector ranking: %v",got)
		}
	}
	assertOrder(publicEmbeddingExactSQL)
	var indexed bool
	if err:=d.Pool.QueryRow(ctx,"SELECT to_regclass('public_post_embeddings_hnsw') IS NOT NULL").Scan(&indexed);err!=nil { t.Fatal(err) }
	if os.Getenv("POSTA_TEST_REQUIRE_PGVECTOR") == "1" && !indexed {
		t.Fatal("pgvector test environment must exercise the actual HNSW index")
	}
	if indexed { assertOrder(publicEmbeddingVectorSQL) }
	changed:=jx.Copy(first)
	changed["views"]=100
	a.storePublicPosts(ctx,PlatformRSS,source,[]jx.M{changed})
	assertOrder(publicEmbeddingExactSQL)
	changed["text"]="History of Rome"
	a.storePublicPosts(ctx,PlatformRSS,source,[]jx.M{changed})
	if got:=find(publicEmbeddingExactSQL);len(got)!=1 || got[0]!=jx.Str(second,"url") {
		t.Fatalf("stale vector: %v",got)
	}
	if save(source,model,first,v(1,0))!=0 {
		t.Fatal("stale in-flight embedding overwrote a newer revision")
	}
}

func TestPublicInventoryTerms(t *testing.T) {
	got:=publicInventoryTerms("SMM",[]string{"smm"," ","social media marketing","продвижение соцсетей","social networks","extra"})
	if len(got)!=4 || got[0]!="SMM" || got[1]!="social media marketing" || got[3]!="social networks" {
		t.Fatalf("unexpected query variants: %v",got)
	}
}
