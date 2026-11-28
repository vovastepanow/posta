package posta

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
	"posta/internal/jx"
)

type InventoryBenchmarkVector struct {
	Model string    `json:"model"`
	Vector []float64 `json:"vector"`
}

type InventoryBenchmarkSample struct {
	ID string `json:"id"`
	URL string `json:"url"`
	Title string `json:"title"`
	Text string `json:"text"`
	Image bool `json:"image"`
	Video bool `json:"video"`
	Audio bool `json:"audio"`
}

type InventoryBenchmarkRanks struct {
	Query string `json:"query"`
	LegacyKey string `json:"legacy_key"`
	VectorModel string `json:"vector_model,omitempty"`
	AsOf int64 `json:"as_of"`
	Baseline []string `json:"baseline"`
	Open []string `json:"open"`
	Control []string `json:"control"`
}

// BuildInventoryBenchmark compares candidate retrieval, not final admission.
// It uses a single repeatable-read public catalog snapshot and no model calls.
// A supplied query vector must match the indexed model exactly; without one
// the open variant measures lexical + observed-media retrieval only.
func (a *App) BuildInventoryBenchmark(ctx context.Context, query, legacyKey string, vector *InventoryBenchmarkVector) (InventoryBenchmarkRanks, []InventoryBenchmarkSample, error) {
	out := InventoryBenchmarkRanks{Query: strings.TrimSpace(query), LegacyKey: strings.TrimSpace(legacyKey), AsOf: now(), Baseline: []string{}, Open: []string{}, Control: []string{}}
	if out.Query == "" || out.LegacyKey == "" {
		return out, nil, fmt.Errorf("query and legacy taxonomy key are required")
	}
	if vector != nil {
		if !strings.HasSuffix(vector.Model, publicEmbeddingVersion) || publicEmbeddingArray(vector.Vector) == nil {
			return out, nil, fmt.Errorf("invalid or incompatible query vector")
		}
		out.VectorModel = vector.Model
	}
	tx, err := a.DB.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback(ctx)
	fresh := &App{DB: a.DB}
	if err = fresh.loadCatalog(ctx, tx); err != nil {
		return out, nil, err
	}
	allowed := fresh.publicEmbeddingSources()
	baseline, err := inventoryBenchmarkRead(ctx, tx, `SELECT p.platform,p.data FROM public_posts p
		JOIN public_post_topics t USING(source_key,post_key)
		WHERE t.topic_key=$1 AND p.source_key=ANY($2::text[]) AND p.last_seen >= $3
		AND `+publicEmbeddingSafeSQL+` ORDER BY p.last_seen DESC,p.source_key,p.post_key LIMIT 100`, out.LegacyKey, allowed, out.AsOf-7*86400)
	if err != nil {
		return out, nil, err
	}
	lexical, err := inventoryBenchmarkRead(ctx, tx, publicInventorySearchSQL, trimRunes(out.Query,300), allowed, 240)
	if err != nil {
		return out, nil, err
	}
	media, err := inventoryBenchmarkRead(ctx, tx, publicMediaSearchSQL, trimRunes(out.Query,300), allowed, 120)
	if err != nil {
		return out, nil, err
	}
	lists := [][]publicInventoryCandidate{lexical, media}
	if vector != nil {
		var indexed bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM public_post_embeddings WHERE model=$1 AND source_key=ANY($2::text[]))`, vector.Model, allowed).Scan(&indexed); err != nil {
			return out, nil, err
		}
		if !indexed {
			return out, nil, fmt.Errorf("no eligible corpus vectors for model %q", vector.Model)
		}
		semantic, err := inventoryBenchmarkRead(ctx, tx, publicEmbeddingExactSQL, vector.Model, allowed, publicEmbeddingArray(vector.Vector), 120)
		if err != nil {
			return out, nil, err
		}
		lists = append(lists, semantic)
	}
	open := rankPublicInventory(lists...)
	control, err := inventoryBenchmarkRead(ctx, tx, `SELECT p.platform,p.data FROM public_posts p
		WHERE p.source_key=ANY($1::text[]) AND `+publicEmbeddingSafeSQL+`
		ORDER BY md5(p.source_key||':'||p.post_key||$2) LIMIT 100`, allowed, out.Query)
	if err != nil {
		return out, nil, err
	}
	samples := map[string]InventoryBenchmarkSample{}
	add := func(rows []publicInventoryCandidate) []string {
		ids := make([]string,0,min(100,len(rows)))
		seen := map[string]bool{}
		for _, row := range rows {
			p := row.post
			key := CandidatePostKey(p)
			url := jx.Str(p,"url")
			if key == "" || sourceReviewSensitive(p) || !autopilotPublicPostURL(url,false) {
				continue
			}
			id := repeatDigest(key)
			if seen[id] {
				continue
			}
			seen[id] = true
			ids = append(ids,id)
			samples[id] = InventoryBenchmarkSample{ID:id, URL:url, Title:trimRunes(jx.Str(p,"title"),600), Text:trimRunes(jx.Str(p,"text"),6000), Image:ContentImageKey(p)!="", Video:PostItemHasVideo(p), Audio:jx.Str(p,"audio")!=""}
			if len(ids)==100 {
				break
			}
		}
		return ids
	}
	out.Baseline,out.Open,out.Control = add(baseline),add(open),add(control)
	blind := make([]InventoryBenchmarkSample,0,len(samples))
	for _, sample := range samples {
		blind = append(blind,sample)
	}
	sort.Slice(blind,func(i,j int)bool{return blind[i].ID<blind[j].ID})
	if err = tx.Commit(ctx); err != nil {
		return out,nil,err
	}
	return out,blind,nil
}

func inventoryBenchmarkRead(ctx context.Context, q db.Querier, sql string, args ...any) ([]publicInventoryCandidate,error) {
	rows,err:=q.Query(ctx,sql,args...)
	if err!=nil {
		return nil,err
	}
	defer rows.Close()
	var out []publicInventoryCandidate
	for rows.Next() {
		var platform string
		var raw []byte
		if err=rows.Scan(&platform,&raw);err!=nil {
			return nil,err
		}
		if p:=jx.Unmarshal(raw);p!=nil {
			out=append(out,publicInventoryCandidate{platform,p})
		}
	}
	return out,rows.Err()
}

type InventoryBenchmarkLabel struct {
	ID string `json:"id"`
	Relevant *bool `json:"relevant"`
}

type InventoryBenchmarkScore struct {
	Returned int `json:"returned"`
	Judged int `json:"judged"`
	Relevant int `json:"relevant"`
	Precision *float64 `json:"judged_precision"`
	Coverage *float64 `json:"judgment_coverage"`
	PooledRecall *float64 `json:"judged_pool_recall"`
}

// Unknown labels are not negative evidence. Recall is explicitly confined to
// the judged candidate/control pool; this cannot measure whole-web recall.
func ScoreInventoryBenchmark(r InventoryBenchmarkRanks, labels []InventoryBenchmarkLabel) (map[string]map[int]InventoryBenchmarkScore,error) {
	known:=map[string]bool{}
	for _, list:=range [][]string{r.Baseline,r.Open,r.Control} {
		for _, id:=range list { known[id]=true }
	}
	judgments:=map[string]*bool{}
	totalRelevant:=0
	for _, label:=range labels {
		if _,duplicate:=judgments[label.ID];duplicate||!known[label.ID] {
			return nil,fmt.Errorf("unknown or repeated annotation %q",label.ID)
		}
		judgments[label.ID]=label.Relevant
		if label.Relevant!=nil && *label.Relevant { totalRelevant++ }
	}
	ratio:=func(n,d int)*float64 {
		if d==0{return nil}
		v:=float64(n)/float64(d)
		return &v
	}
	out:=map[string]map[int]InventoryBenchmarkScore{}
	for name,list:=range map[string][]string{"baseline":r.Baseline,"open":r.Open,"control":r.Control} {
		out[name]=map[int]InventoryBenchmarkScore{}
		for _,k:=range []int{10,20,100} {
			var s InventoryBenchmarkScore
			seen:=map[string]bool{}
			for _,id:=range list {
				if seen[id]{continue}
				seen[id]=true
				s.Returned++
				if v:=judgments[id];v!=nil {
					s.Judged++
					if *v{s.Relevant++}
				}
				if s.Returned==k{break}
			}
			s.Precision=ratio(s.Relevant,s.Judged)
			s.Coverage=ratio(s.Judged,s.Returned)
			s.PooledRecall=ratio(s.Relevant,totalRelevant)
			out[name][k]=s
		}
	}
	return out,nil
}
