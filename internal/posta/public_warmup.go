package posta

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
)

// Starter queries are scheduling hints, not mandatory labels for posts.
// A user can search for anything outside this list. Existing topic keys
// remain a compatibility bridge while the old assessment path is migrated.
var publicWarmSeedQueries = []string{
	"Мемы", "SMM и блогинг", "Животные", "Бизнес",
	"Технологии", "Новости", "Юмор", "Автомобили",
	"Психология и саморазвитие", "Здоровье и фитнес", "Еда", "Игры",
	"Деньги и инвестиции", "Путешествия", "Кино и сериалы", "Нейросети",
	"Дом и сад", "Хобби", "Мода и красота", "Наука и образование",
}

func (a *App) seedPublicWarmProfiles(ctx context.Context) error {
	keys := make([]string,len(publicWarmSeedQueries))
	for i,q := range publicWarmSeedQueries {
		keys[i] = autopilotTopicKey(q)
	}
	_,err := a.DB.Pool.Exec(ctx,`INSERT INTO public_warm_profiles(topic_key,query,start_after)
		SELECT key,query,$3+(ord-1)*900
		FROM unnest($1::text[],$2::text[]) WITH ORDINALITY AS s(key,query,ord)
		ON CONFLICT(topic_key) DO NOTHING`,keys,publicWarmSeedQueries,now())
	return err
}

type publicWarmSupply struct {
	Target, Ready, Duplicates, MissingMedia, PendingSources, ActiveSources int
}

func (s publicWarmSupply) diagnosis() string {
	switch {
	case s.Ready >= s.Target:
		return "ready"
	case s.MissingMedia > 0 && s.Ready+s.MissingMedia >= s.Target:
		return "missing_media"
	case s.Duplicates > 0 && s.Ready+s.Duplicates >= s.Target:
		return "duplicates"
	case s.PendingSources > 0:
		return "needs_review"
	case s.ActiveSources == 0:
		return "no_sources"
	case s.MissingMedia > 0:
		return "missing_media"
	default:
		return "needs_content"
	}
}

// Count only independently accepted, current-version, publicly accessible
// results. Raw posts or an approved publisher do not satisfy the warm buffer.
// This is a shared editorial baseline; individual users still need their own
// admission, freshness, display and repeat checks before delivery.
func (a *App) refreshPublicWarmSupply(ctx context.Context, job *autopilotJob) (publicWarmSupply,error) {
	var stats publicWarmSupply
	err := a.DB.Pool.QueryRow(ctx,`SELECT target FROM public_warm_profiles WHERE topic_key=$1 AND enabled`,job.TopicKey).Scan(&stats.Target)
	if errors.Is(err,pgx.ErrNoRows) {
		return stats,errAutopilotLeaseLost
	}
	if err != nil {
		return stats,err
	}
	if a.LLM == nil {
		return stats,errors.New("warmup model configuration unavailable")
	}
	at:=time.Now()
	rows,err:=a.DB.Pool.Query(ctx,`SELECT r.source_id,r.post,r.analysis,false AS direct,r.published_at
		FROM source_autopilot_content_results r
		JOIN source_autopilot_sources s USING(scope,topic_key,source_id)
		WHERE r.scope='public' AND r.topic_key=$1 AND r.policy_version=$2
		AND r.published_at >= $3 AND s.state IN ('probation','active')
		AND r.assessed_at >= COALESCE((s.evidence->>'full_recheck_at')::bigint,0)
		AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE o.scope='public'
			AND o.owner_id='' AND o.topic_key=r.topic_key AND o.source_id=r.source_id AND o.decision='excluded')
		UNION ALL
		SELECT r.source_id,r.post,r.analysis,true,r.published_at
		FROM source_autopilot_direct_results r WHERE r.scope='public' AND r.topic_key=$1
		AND r.policy_version=$2 AND r.published_at >= $4 AND r.assessed_at >= $4
		AND NOT EXISTS (SELECT 1 FROM source_autopilot_overrides o WHERE o.scope='public'
			AND o.owner_id='' AND o.topic_key=r.topic_key AND o.source_id=r.source_id AND o.decision='excluded')
		ORDER BY published_at DESC LIMIT 3000`,job.TopicKey,TopicProfileVersion,at.Add(-time.Duration(config.ContentMaxAgeDays)*24*time.Hour).Unix(),at.Add(-autopilotDirectMaxAge).Unix())
	if err != nil {
		return stats,err
	}
	type result struct {
		source string
		post,analysis jx.M
		direct bool
	}
	var results []result
	for rows.Next() {
		var source string
		var raw,analysis []byte
		var direct bool
		var published int64
		if err:=rows.Scan(&source,&raw,&analysis,&direct,&published);err!=nil {
			rows.Close()
			return stats,err
		}
		results=append(results,result{source,jx.Unmarshal(raw),jx.Unmarshal(analysis),direct})
	}
	err=rows.Err()
	rows.Close()
	if err!=nil { return stats,err }
	u:=a.User(ctx,"",nil)
	var valid []jx.M
	for _,r:=range results {
		if ctx.Err()!=nil { return stats,ctx.Err() }
		p,analysis:=r.post,r.analysis
		if p==nil || analysis==nil || sourceReviewSensitive(p) || jx.Bool(p,"text_incomplete") ||
			jx.Bool(p,"media_incomplete") || jx.Bool(p,"video_unavailable") || jx.Bool(analysis,"tags_classification_pending") ||
			jx.Str(analysis,"model")=="" || jx.Int(p,"posted_at")<=0 || jx.Int(p,"posted_at")>at.Unix()+300 ||
			!autopilotFormatAllowsItem(u,job.Topic,p) ||
			u.contentAdmissionReason(analysis,p,job.Topic,AnalysisText(p))!="" {
			continue
		}
		if r.direct && !autopilotDirectPostEligible(u,r.source,job.Topic,p,analysis,at.Unix()) {
			continue
		}
		video,image:=PostItemHasVideo(p),ContentImageKey(p)!=""
		if video {
			if jx.Str(p,"video")=="" || a.MediaExisting(jx.Str(p,"video_local"))=="" {
				stats.MissingMedia++
				continue
			}
		} else if image {
			if a.MediaExisting(jx.Str(p,"image_local"))=="" {
				stats.MissingMedia++
				continue
			}
		} else if !u.TBool("text_posts") || !u.TopicsAcceptText([]string{TopicBase(job.Topic)}) || AnalysisText(p)=="" {
			continue
		}
		if len(repeatKeys(p))>0 {
			valid=append(valid,p)
		}
	}
	stats.Ready=autopilotSupplyCount(valid,nil,func(jx.M)bool{return true},func(jx.M)bool{return true})
	stats.Duplicates=len(valid)-stats.Ready
	err=a.DB.Pool.QueryRow(ctx,`SELECT count(*) FILTER(WHERE state IN ('candidate','probation')),
		count(*) FILTER(WHERE state IN ('active','probation'))
		FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1`,job.TopicKey).Scan(&stats.PendingSources,&stats.ActiveSources)
	if err!=nil { return stats,err }
	err=withAutopilotLease(ctx,a.DB,job,func(tx pgx.Tx)error{
		if err:=ensureAutopilotShadowDemand(ctx,tx,job);err!=nil{return err}
		_,err:=tx.Exec(ctx,`UPDATE public_warm_profiles SET checked_at=$2,ready_count=$3,
			duplicate_count=$4,missing_media_count=$5,diagnosis=$6 WHERE topic_key=$1 AND enabled`,
			job.TopicKey,at.Unix(),stats.Ready,stats.Duplicates,stats.MissingMedia,stats.diagnosis())
		return err
	})
	return stats,err
}

func publicWarmInterval(ready,target int) time.Duration {
	if ready>=target { return 6*time.Hour }
	return 2*time.Hour
}

// Warm jobs cannot exhaust the main server's quota before a real user arrives.
// The limits are applied to the existing common daily ledger, not a bypass.
func publicWarmBudget(serverModel,topicModel,serverFetch,topicFetch,freeDaily int) (int,int,int,int) {
	modelCap:=min(100,max(1,serverModel/10))
	if freeDaily>0 { modelCap=min(modelCap,max(1,freeDaily/10)) }
	return min(serverModel,modelCap),min(topicModel,30),
		min(serverFetch,min(500,max(1,serverFetch/5))),min(topicFetch,150)
}
