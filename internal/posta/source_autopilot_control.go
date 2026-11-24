package posta

import (
	"context"
	"time"

	"posta/internal/jx"
)

// A known publisher is a useful access/model control, not a gold label for
// every post. Keep this panel strictly separate from the discovery algorithm.
var autopilotSMMControlFeeds = []struct{ name, raw string }{
	{"Buffer", "https://buffer.com/resources/feed"},
	{"Hootsuite", "https://blog.hootsuite.com/feed/"},
	{"Social Media Examiner", "https://www.socialmediaexaminer.com/feed/"},
	{"Sprout Social", "https://sproutsocial.com/insights/feed/"},
	{"Social Media Today", "https://www.socialmediatoday.com/feeds/news/"},
	{"Metricool", "https://metricool.com/feed/"},
	{"HubSpot Marketing", "https://blog.hubspot.com/marketing/rss.xml"},
	{"SMMplanner", "https://smmplanner.com/blog/rss/"},
}

const autopilotControlPanelKey = "smm_control_audit_v1"

// Run at most one publisher in a cycle and no more often than 15 minutes.
// Only the diagnostic checkpoint is changed. No source, evidence, cache
// media, direct result, projection, or subscriber pool is created.
func (a *App) autopilotRunControlAudit(ctx context.Context, job *autopilotJob) error {
	if !validAutopilotPublicJob(job) || job.Scope != autopilotAutoScope ||
		autopilotCanonicalTopic(job.Topic) != "SMM и блогинг" ||
		a.LLM == nil || !a.TBool("autopilot_control_audit") {
		return nil
	}
	if err := autopilotBudgetError(ctx); err != nil {
		return err
	}
	checkpoint := jx.Unmarshal(job.Checkpoint)
	if checkpoint == nil {
		checkpoint = jx.M{}
	}
	panel := jx.Map(checkpoint, autopilotControlPanelKey)
	if panel == nil {
		panel = jx.M{}
	}
	at := time.Now()
	if jx.Int(panel, "last_attempt_at") > at.Add(-15*time.Minute).Unix() {
		return nil
	}
	results := jx.Map(panel, "results")
	if results == nil {
		results = jx.M{}
	}
	index := autopilotControlNextPublisher(results, at)
	if index < 0 {
		return nil
	}
	f := autopilotSMMControlFeeds[index]
	result, err := a.autopilotControlPublisher(ctx, job, f.raw, at)
	if err != nil {
		return err
	}
	previous := jx.Map(results, f.name)
	if len(previous) > 0 {
		result["attempts"] = 2
		result["first_status"] = jx.Str(previous, "status")
		result["first_preliminary_completed"] = jx.Bool(previous, "preliminary_completed")
		result["first_post_status"] = jx.Str(jx.Map(previous, "post_probe"), "status")
	} else {
		result["attempts"] = 1
	}
	results[f.name] = result
	panel["results"], panel["last_attempt_at"] = results, at.Unix()
	checkpoint[autopilotControlPanelKey] = panel
	job.Checkpoint = jx.Marshal(checkpoint)
	return saveAutopilotCheckpoint(ctx, a.DB, job)
}

// Complete the first panel before spending one bounded retry on an
// incomplete reading or model response. A genuine negative completed
// assessment is not retried simply because the score was low.
func autopilotControlNextPublisher(results jx.M, at time.Time) int {
	for i, f := range autopilotSMMControlFeeds {
		if len(jx.Map(results, f.name)) == 0 {
			return i
		}
	}
	for i, f := range autopilotSMMControlFeeds {
		result := jx.Map(results, f.name)
		if jx.Int(result, "attempts") >= 2 || jx.Int(result, "at") > at.Add(-time.Hour).Unix() {
			continue
		}
		probe := jx.Map(result, "post_probe")
		if jx.Str(result, "status") != "readable" ||
			(jx.Int(result, "fresh") > 0 && (!jx.Bool(result, "preliminary_completed") ||
				jx.Int(probe, "completed") < jx.Int(probe, "submitted") ||
				jx.Str(probe, "status") == "provider_failure")) {
			return i
		}
	}
	return -1
}

func (a *App) autopilotControlPublisher(ctx context.Context, job *autopilotJob, feed string, at time.Time) (jx.M, error) {
	ctx = withAutopilotAssessmentProbe(ctx)
	u := a.User(ctx, "", nil)
	res := autopilotReadRSS(feed, u.RSSGet)
	if err := autopilotBudgetError(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := jx.M{"at": at.Unix(), "http_status": res.Status, "status": "not_readable"}
	if !res.OK || !rssIsFeed(rssLoad(res.Bytes)) {
		return result, nil
	}
	posts, err := RSSParse(res.Bytes, 20, "", res.URL, true, true, 0)
	if err != nil {
		result["status"] = "parse_failed"
		return result, nil
	}
	quality := autopilotSourceQualitySamples(posts, at.Unix())
	safe := quality[:0]
	for _, p := range quality {
		if !jx.Bool(p, "source_sensitive") && !jx.Bool(p, "nsfw") && len(jx.ToStrs(p["safety"])) == 0 && !a.ContentIsAdult(p) {
			safe = append(safe, p)
		}
	}
	fresh := autopilotFreshRSSSamples(safe, at.Unix())
	result["status"], result["parsed"], result["fresh"] = "readable", len(posts), len(fresh)
	if len(safe) == 0 {
		return result, nil
	}
	info := autopilotSourceReviewInfo(PlatformRSS, feed, feed, res, safe)
	evaluation := u.autopilotEvaluateSource(info, safe, job.Topic)
	if err := autopilotBudgetError(ctx); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if evaluation != nil {
		pass, reject := autopilotSourcePreliminaryDecision(evaluation, a.TInt("autopilot_preliminary_score"), len(safe))
		result["preliminary_completed"], result["preliminary_score"] = true, evaluation.Score
		result["preliminary_pass"], result["preliminary_reject"] = pass, reject
	}
	probe, err := a.autopilotProbeTextSample(ctx, job, "rss:"+feed, fresh)
	if err != nil {
		return nil, err
	}
	result["post_probe"] = probe
	return result, nil
}

// Explicit whitelist; no raw response, post, link, arbitrary model error,
// or nested checkpoint data is exposed to a subscriber.
func autopilotControlPanelSummary(checkpoint jx.M) jx.M {
	panel := jx.Map(checkpoint, autopilotControlPanelKey)
	results := jx.Map(panel, "results")
	out := jx.M{"panel_size": len(autopilotSMMControlFeeds), "last_attempt_at": jx.Int(panel, "last_attempt_at"), "independent_quality_measured": false}
	var details []jx.M
	add := func(key string, n int64) { out[key] = jx.Int(out, key) + n }
	for _, f := range autopilotSMMControlFeeds {
		result := jx.Map(results, f.name)
		if len(result) == 0 {
			continue
		}
		add("attempted", 1)
		attempts := max(int64(1), jx.Int(result, "attempts"))
		add("attempts", attempts)
		item := jx.M{"publisher": f.name, "at": jx.Int(result, "at"), "http_status": jx.Int(result, "http_status"), "attempts": attempts}
		if attempts > 1 {
			item["first_status"] = jx.Str(result, "first_status")
			item["first_preliminary_completed"] = jx.Bool(result, "first_preliminary_completed")
			item["first_post_status"] = jx.Str(result, "first_post_status")
		}
		switch status := jx.Str(result, "status"); status {
		case "readable", "not_readable", "parse_failed":
			item["status"] = status
		}
		if jx.Str(result, "status") == "readable" {
			add("readable", 1)
			item["fresh"] = jx.Int(result, "fresh")
		}
		if jx.Bool(result, "preliminary_completed") {
			add("preliminary_completed", 1)
			item["preliminary_score"] = jx.Int(result, "preliminary_score")
			item["preliminary_pass"] = jx.Bool(result, "preliminary_pass")
			if jx.Bool(result, "preliminary_pass") {
				add("preliminary_pass", 1)
			}
		}
		probe := jx.Map(result, "post_probe")
		for _, key := range []string{"submitted", "completed", "eligible"} {
			n := max(int64(0), jx.Int(probe, key))
			add(key, n)
			item[key] = n
		}
		if jx.Int(probe, "eligible") > 0 {
			add("sources_with_eligible", 1)
		}
		switch status := jx.Str(probe, "status"); status {
		case "completed_or_partial", "provider_failure", "no_text_only_input":
			item["post_status"] = status
		}
		details = append(details, item)
	}
	out["publishers"] = details
	return out
}
