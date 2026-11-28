package posta

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
)

func TestPublicWarmupPolicy(t *testing.T) {
	seen:=map[string]bool{}
	for _,q:=range publicWarmSeedQueries {
		k:=autopilotTopicKey(q)
		if k=="" || seen[k] { t.Fatalf("duplicate starter query: %s",q) }
		seen[k]=true
	}
	if len(seen)!=20 {t.Fatalf("starter query count: %d",len(seen))}
	for _,tc:=range []struct{s publicWarmSupply;want string}{
		{publicWarmSupply{Target:100,Ready:100},"ready"},
		{publicWarmSupply{Target:100,Ready:80,Duplicates:20},"duplicates"},
		{publicWarmSupply{Target:100,Ready:80,MissingMedia:20},"missing_media"},
		{publicWarmSupply{Target:100,PendingSources:4},"needs_review"},
		{publicWarmSupply{Target:100},"no_sources"},
		{publicWarmSupply{Target:100,ActiveSources:4},"needs_content"},
	} {
		if got:=tc.s.diagnosis();got!=tc.want{t.Fatalf("diagnosis %s want %s",got,tc.want)}
	}
	sm,tm,sf,tf:=publicWarmBudget(5000,2000,10000,3000,1000)
	if sm!=100||tm!=30||sf!=500||tf!=150{t.Fatalf("warmup caps: %d %d %d %d",sm,tm,sf,tf)}
	sm,_,_,_=publicWarmBudget(5000,2000,10000,3000,7)
	if sm!=1 {t.Fatalf("small provider quota not protected: %d",sm)}
}

func TestPublicWarmupDemandPostgres(t *testing.T) {
	dsn:=os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn==""{t.Skip("requires isolated PostgreSQL")}
	ctx:=context.Background()
	d,err:=db.Open(ctx,dsn)
	if err!=nil{t.Fatal(err)}
	defer d.Close()
	if _,err=d.Migrate(ctx);err!=nil{t.Fatal(err)}
	a:=&App{DB:d}
	suffix:=randHex(6)
	warmTopic,ownerTopic:="Warmup test "+suffix,"Owner test "+suffix
	warmKey,ownerKey:=autopilotTopicKey(warmTopic),autopilotTopicKey(ownerTopic)
	owner:="warm-test-"+suffix
	defer d.Pool.Exec(ctx,`DELETE FROM users WHERE id=$1`,owner)
	defer d.Pool.Exec(ctx,`DELETE FROM public_warm_profiles WHERE topic_key=$1`,warmKey)
	defer d.Pool.Exec(ctx,`DELETE FROM source_autopilot_jobs WHERE scope='public' AND topic_key=ANY($1::text[])`,[]string{warmKey,ownerKey})
	if _,err=d.Pool.Exec(ctx,`INSERT INTO public_warm_profiles(topic_key,query) VALUES($1,$2)`,warmKey,warmTopic);err!=nil{t.Fatal(err)}
	if _,err=d.Pool.Exec(ctx,`INSERT INTO users(id,created_at) VALUES($1,0)`,owner);err!=nil{t.Fatal(err)}
	if err=saveAutopilotDemand(ctx,d.Pool,owner,autopilotDemandSettings{Topic:ownerTopic,Mode:"auto",PostsPerDay:2,HorizonDays:3},now());err!=nil{t.Fatal(err)}
	if err=a.reconcileAutopilotMode(ctx,true,"auto",autopilotAutoScope,autopilotAutoKind);err!=nil{t.Fatal(err)}
	at:=time.Now()
	if _,err=d.Pool.Exec(ctx,`UPDATE source_autopilot_jobs SET next_run_at=CASE WHEN topic_key=$1 THEN $3 ELSE $4 END
		WHERE scope='public' AND topic_key=ANY($2::text[])`,warmKey,[]string{warmKey,ownerKey},at.Add(-1000*time.Hour).Unix(),at.Add(-900*time.Hour).Unix());err!=nil{t.Fatal(err)}
	job,err:=claimAutopilotJobKind(ctx,d.Pool,at,time.Minute,autopilotAutoKind)
	if err!=nil||job==nil||job.TopicKey!=ownerKey{t.Fatalf("warmup starved user: %+v %v",job,err)}
	if err=finishAutopilotJob(ctx,d,job,at.Add(time.Hour),job.Checkpoint,"");err!=nil{t.Fatal(err)}
	job,err=claimAutopilotJobKind(ctx,d.Pool,at,time.Minute,autopilotAutoKind)
	if err!=nil||job==nil||job.TopicKey!=warmKey{t.Fatalf("ownerless warmup job missing: %+v %v",job,err)}
	if err=withAutopilotLease(ctx,d,job,func(tx pgx.Tx)error{return ensureAutopilotShadowDemand(ctx,tx,job)});err!=nil{t.Fatalf("warmup demand rejected: %v",err)}
	if _,err=d.Pool.Exec(ctx,`UPDATE public_warm_profiles SET enabled=false WHERE topic_key=$1`,warmKey);err!=nil{t.Fatal(err)}
	if err=a.reconcileAutopilotMode(ctx,true,"auto",autopilotAutoScope,autopilotAutoKind);err!=nil{t.Fatal(err)}
	if err=withAutopilotLease(ctx,d,job,func(tx pgx.Tx)error{return ensureAutopilotShadowDemand(ctx,tx,job)});!errors.Is(err,errAutopilotLeaseLost){t.Fatalf("disabled warmup retains lease: %v",err)}
}
