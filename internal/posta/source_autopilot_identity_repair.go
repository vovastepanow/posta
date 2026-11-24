package posta

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

// Historical audit leads, not trusted IDs or source approvals. Every lead
// must be checked against the official Atom feed before it may repair an
// existing rejected lowercase identity. No topic-specific behavior.
var autopilotYouTubeRecoveryLeads = []string{
	"UC2Rt4-M51tdAIYOD6uBOZ6w",
	"UC4mO3iaIrA9mZI3WUEjiubw",
	"UC6IDxt7jtymev4299tLhW_g",
	"UCby8Hqx2qogm8I85ndrHtMA",
	"UCCTd_Pod3I2-X62y_PrCLSA",
	"UCDCbdigxpip3hW-1gmBGdfg",
	"UCEgQKCxk91N8Y19DSqiDOpg",
	"UCgVJE-ez4bCEj8ILrM7g5sQ",
	"UCHb8IB3bh9uXpKL2KwY7viA",
	"UCiJVR2rgfxY0PwNzI0rK_pQ",
	"UCi_zIYb69eHcdy0e2rlMotw",
	"UCjPiNc_0J6iq5rPBnx-cI6g",
	"UCm0JN7C90ysi2STqCXvZzBg",
	"UCq2GZvhTAiGbmYTFHa_Zrhg",
	"UCR0djoCNqqYcDLVjluHBL0w",
	"UCVtFh_62yrW-vj95T0TvYiw",
	"UCZlFmJ-8aj_wch2T2CWoe8w",
	"UCLJ8r2PpeJtDs716V4YaO9A",
	"UCw0r50dHcAHmAdPaw36O8Xw",
}

func autopilotYouTubeLostKey(old string) string {
	if !strings.HasPrefix(old,"rss:") {
		return ""
	}
	raw:=strings.TrimPrefix(old,"rss:")
	p,err:=url.Parse(raw)
	if err!=nil || p.Scheme!="https" || p.User!=nil || p.Port()!="" || p.Fragment!="" || p.Path!="/feeds/videos.xml" || (p.Host!="www.youtube.com" && p.Host!="youtube.com") {
		return ""
	}
	q,err:=url.ParseQuery(p.RawQuery)
	if err!=nil || len(q)!=1 || len(q["channel_id"])!=1 {
		return ""
	}
	id:=q.Get("channel_id")
	if id!=strings.ToLower(id) || !strings.HasPrefix(id,"uc") || !ytChannelIDRe.MatchString("UC"+id[2:]) {
		return ""
	}
	return id
}

func autopilotYouTubeRecoveryFeed(old string, leads []string) string {
	key:=autopilotYouTubeLostKey(old)
	if key=="" {
		return ""
	}
	found:=""
	for _,id:=range leads {
		if !ytChannelIDRe.MatchString(id) || strings.ToLower(id)!=key {
			continue
		}
		if found!="" && found!=id {
			return ""
		}
		found=id
	}
	if found=="" {
		return ""
	}
	return YouTubeChannelFeedURL(found)
}

func autopilotVerifiedYouTubeIdentity(feed string, res FetchResultURL) bool {
	canonical,ok:=canonicalYouTubeFeed(feed)
	final,finalOK:=canonicalYouTubeFeed(res.URL)
	if !ok || !finalOK || canonical!=feed || final!=feed || !res.OK {
		return false
	}
	doc:=rssLoad(res.Bytes)
	if doc==nil || doc.N!="feed" {
		return false
	}
	// YouTube's feed-level channelId may omit the fixed UC prefix,
	// unlike entry-level channelId. Never change the remaining case.
	id := strings.TrimSpace(doc.kid("yt:channelId").text())
	if len(id) == 22 {
		id = "UC" + id
	}
	if !ytChannelIDRe.MatchString(id) || YouTubeChannelFeedURL(id) != feed {
		return false
	}
	// Require independent matching evidence from entries or the
	// official alternate channel URL, including for empty channels.
	entries := doc.kids("entry")
	for _, entry := range entries {
		if strings.TrimSpace(entry.kid("yt:channelId").text()) != id {
			return false
		}
	}
	if len(entries) > 0 {
		return true
	}
	for _, link := range doc.kids("link") {
		if link.A["rel"] == "alternate" && link.A["href"] == "https://www.youtube.com/channel/"+id {
			return true
		}
	}
	return false
}

// Preserve the old decision, including manual overrides. The verified
// replacement is merely a candidate; all normal model gates still apply.
func (a *App) autopilotRepairVerifiedSource(ctx context.Context,job *autopilotJob,old,corrected string,at time.Time) error {
	if old==corrected {
		return nil
	}
	if _,_,_,ok:=autopilotSourceParts(corrected); !ok {
		return nil
	}
	return withAutopilotLease(ctx,a.DB,job,func(tx pgx.Tx) error {
		if err:=ensureAutopilotShadowDemand(ctx,tx,job);err!=nil {
			return err
		}
		first,second:=old,corrected
		if second<first {
			first,second=second,first
		}
		for _,id:=range []string{first,second} {
			if err:=lockAutopilotSource(ctx,tx,job.Scope,job.TopicKey,id);err!=nil {
				return err
			}
		}
		var excluded bool
		if err:=tx.QueryRow(ctx,`SELECT EXISTS(SELECT 1 FROM source_autopilot_overrides WHERE (scope=$1 OR ($1='public:shadow' AND scope='public')) AND topic_key=$2 AND source_id=ANY($3::text[]) AND decision='excluded')`,job.Scope,job.TopicKey,[]string{old,corrected}).Scan(&excluded);err!=nil {
			return err
		}
		if excluded {
			return nil
		}
		evidence:=jx.Marshal(jx.M{"technical_repair_v2":true,"replacement_source_id":corrected})
		_,err:=tx.Exec(ctx,`WITH repaired AS (
			UPDATE source_autopilot_sources SET reason='source_identity_recovered', evidence=evidence||$5::jsonb,checked_at=$6,next_check_at=$7
			WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND state='rejected'
			AND reason IN ('unsupported_source_format','invalid_source_identity') RETURNING source_id
		), inserted AS (
			INSERT INTO source_autopilot_sources(scope,topic_key,source_id,state,reason,policy_version,first_seen_at,evidence)
			SELECT $1,$2,$4,'candidate','discovered',$8,$6,jsonb_build_object('recovered_from',$3::text)
			FROM repaired ON CONFLICT DO NOTHING RETURNING source_id
		), history AS (
			INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at,evidence)
			SELECT $1,$2,source_id,'rejected','rejected','source_identity_recovered',$8,$6,$5::jsonb FROM repaired
		)
		INSERT INTO source_autopilot_decisions(scope,topic_key,source_id,old_state,new_state,reason,policy_version,at,evidence)
		SELECT $1,$2,source_id,'','candidate','discovered',$8,$6,jsonb_build_object('recovered_from',$3::text) FROM inserted`,
			job.Scope,job.TopicKey,old,corrected,evidence,at.Unix(),time.Date(2100,1,1,0,0,0,0,time.UTC).Unix(),TopicProfileVersion)
		return err
	})
}

// Use only a few requests per run. Unknown IDs are never brute-forced.
// Failed reads are retryable and never counted as thematic rejections.
func (a *App) autopilotRecoverLegacyYouTube(ctx context.Context,job *autopilotJob) error {
	rows,err:=a.DB.Pool.Query(ctx,`SELECT source_id FROM source_autopilot_sources
		WHERE scope=$1 AND topic_key=$2 AND state='rejected' AND reason IN ('unsupported_source_format','invalid_source_identity')
		AND source_id LIKE 'rss:https://%youtube.com/feeds/videos.xml?channel_id=uc%'
		AND COALESCE((evidence->>'identity_retry_at')::bigint,0)<=$3 ORDER BY source_id LIMIT 100`,job.Scope,job.TopicKey,time.Now().Unix())
	if err!=nil {
		return err
	}
	var oldIDs []string
	for rows.Next() {
		var old string
		if err:=rows.Scan(&old);err!=nil {
			rows.Close()
			return err
		}
		oldIDs=append(oldIDs,old)
	}
	err=rows.Err()
	rows.Close()
	if err!=nil {
		return err
	}
	// Exact identities rediscovered by any topic are also recovery leads.
	leads:=append([]string(nil),autopilotYouTubeRecoveryLeads...)
	if len(oldIDs)==0 {
		return nil
	}
	known,err:=a.DB.Pool.Query(ctx,`SELECT DISTINCT source_id FROM source_autopilot_sources WHERE source_id LIKE 'rss:https://www.youtube.com/feeds/videos.xml?channel_id=UC%' LIMIT 10000`)
	if err!=nil {
		return err
	}
	for known.Next() {
		var source string
		if err:=known.Scan(&source);err!=nil {
			known.Close()
			return err
		}
		if feed,ok:=canonicalYouTubeFeed(strings.TrimPrefix(source,"rss:"));ok {
			p,_:=url.Parse(feed)
			leads=append(leads,p.Query().Get("channel_id"))
		}
	}
	err=known.Err()
	known.Close()
	if err!=nil {
		return err
	}
	u:=a.User(ctx,"",nil)
	attempts:=0
	for _,old:=range oldIDs {
		feed:=autopilotYouTubeRecoveryFeed(old,leads)
		if feed=="" {
			continue
		}
		if attempts>=3 || ctx.Err()!=nil || autopilotBudgetError(ctx)!=nil {
			break
		}
		attempts++
		r:=u.RSSGet(feed,rssMaxBytes,10*time.Second,HTTPUA)
		if err:=autopilotBudgetError(ctx);err!=nil {
			return err
		}
		if autopilotVerifiedYouTubeIdentity(feed,r) {
			if err:=a.autopilotRepairVerifiedSource(ctx,job,old,"rss:"+feed,time.Now());err!=nil {
				return err
			}
		}
		if err:=withAutopilotLease(ctx,a.DB,job,func(tx pgx.Tx)error{
			_,err:=tx.Exec(ctx,`UPDATE source_autopilot_sources SET evidence=evidence||jsonb_build_object('identity_retry_at',$4::bigint)
				WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND state='rejected' AND reason IN ('unsupported_source_format','invalid_source_identity')`,job.Scope,job.TopicKey,old,time.Now().Add(24*time.Hour).Unix())
			return err
		});err!=nil {
			return err
		}
		if r.Status==429 {
			break
		}
	}
	return nil
}
