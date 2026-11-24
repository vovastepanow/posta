package posta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

func autopilotPublicPostURL(raw string, media bool) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && (media || u.Scheme != "http")) || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return false
	}
	if media && u.RawQuery != "" {
		return false
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for key := range query {
		key = strings.ToLower(key)
		if key == "auth" || key == "authorization" || key == "sig" {
			return false
		}
		for _, sensitive := range []string{"token", "secret", "password", "signature", "credential", "api_key", "apikey"} {
			if strings.Contains(key, sensitive) {
				return false
			}
		}
	}
	return true
}

func (a *App) autopilotHasSavedResult(ctx context.Context, job *autopilotJob, source, postHash string) (bool, error) {
	if job.Scope != autopilotAutoScope {
		return true, nil
	}
	var saved bool
	err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_content_results WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND assessed_at >= `+autopilotFullRecheckEpochSQL+`)`, job.Scope, job.TopicKey, source, postHash, TopicProfileVersion).Scan(&saved)
	return saved, err
}

// Revalidate unchanged media without paying for the same model analysis again.
// A failed fetch leaves it unknown; changed bytes require a new assessment.
func (a *App) refreshAutopilotMedia(ctx context.Context, job *autopilotJob, source, postHash string, post jx.M, video bool) (bool, error) {
	if job.Scope != autopilotAutoScope {
		return true, nil
	}
	var raw []byte
	err := a.DB.Pool.QueryRow(ctx, `SELECT post FROM source_autopilot_content_results WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND assessed_at >= `+autopilotFullRecheckEpochSQL+``, job.Scope, job.TopicKey, source, postHash, TopicProfileVersion).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	saved := jx.Unmarshal(raw)
	localField := "image_local"
	if video {
		localField = "video_local"
	}
	if a.MediaExisting(jx.Str(saved, localField)) != "" {
		if video && a.MediaExisting(jx.Str(saved, "image_local")) == "" {
			return false, nil
		}
		return true, nil
	}
	hashes := jx.Strs(saved, "repeat_file_hashes")
	if len(hashes) == 0 {
		return false, nil
	}
	u := a.User(ctx, "", nil)
	var media []byte
	if !video {
		media, _, err = u.autopilotReadPostImage(post)
	} else {
		rawURL := jx.Str(post, "video")
		_, _, tiktokPage := autopilotTikTokVideoParts(jx.Str(post, "video_page"))
		if autopilotYouTubeVideoID(jx.Str(post, "video_page")) != "" || tiktokPage {
			var f *os.File
			f, err = os.CreateTemp("", "posta-autopilot-youtube-*")
			if err == nil {
				path := f.Name()
				err = f.Close()
				if err == nil {
					if tiktokPage {
						err = u.autopilotDownloadTikTok(jx.Str(post, "video_page"), path)
					} else {
						err = u.autopilotDownloadYouTube(jx.Str(post, "video_page"), path)
					}
				}
				if err == nil {
					media, err = os.ReadFile(path)
				}
				_ = os.Remove(path)
			}
		} else {
			for hop := 0; hop <= 3; hop++ {
				var result FetchResult
				result, err = u.FetchPublic(rawURL, FetchOpts{MaxBytes: publicClassificationVideoMaxBytes, Timeout: 90 * time.Second})
				if err != nil {
					break
				}
				if result.Status >= 300 && result.Status < 400 && result.Location != "" {
					rawURL = RSSAbsoluteURL(rawURL, result.Location)
					continue
				}
				if result.OK {
					media = result.Bytes
				}
				break
			}
		}
	}
	if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
		return true, budgetErr
	}
	if err != nil || len(media) == 0 {
		return true, nil
	}
	digest := sha256.Sum256(media)
	if hex.EncodeToString(digest[:]) != hashes[0] {
		return false, nil
	}
	local, err := a.cacheAutopilotMedia(ctx, job, source, hashes[0], media, video)
	if err != nil {
		return true, err
	}
	if local == "" {
		return true, nil
	}
	saved[localField] = local
	if video && a.MediaExisting(jx.Str(saved, "image_local")) == "" {
		return false, nil
	}
	err = withAutopilotLease(ctx, a.DB, job, func(tx pgx.Tx) error {
		if err := ensureAutopilotShadowDemand(ctx, tx, job); err != nil {
			return err
		}
		if err := lockAutopilotSource(ctx, tx, job.Scope, job.TopicKey, source); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE source_autopilot_content_results SET assessed_at=$5,post=$7 WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$6 AND EXISTS(SELECT 1 FROM source_autopilot_sources WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND state IN ('probation','active'))`, job.Scope, job.TopicKey, source, postHash, time.Now().Unix(), TopicProfileVersion, jx.Marshal(saved))
		return err
	})
	return true, err
}

// Rotate each media lane independently across successful feed passes. Otherwise
// the first already-reviewed or inaccessible image/video consumes the same
// bounded attempt slot on every pass, starving the rest of an unchanged feed.
// Text posts keep their positions and order; the original sample is unchanged.
func autopilotRotateMediaReview(posts []jx.M, pass int) []jx.M {
	var images, videos []int
	for i, post := range posts {
		if PostItemHasVideo(post) {
			videos = append(videos, i)
		} else if ContentImageKey(post) != "" {
			images = append(images, i)
		}
	}
	if len(images) <= 2 && len(videos) <= 1 {
		return posts
	}
	rotated := append([]jx.M(nil), posts...)
	for _, lane := range [][]int{images, videos} {
		if len(lane) == 0 {
			continue
		}
		offset := max(0, pass) % len(lane)
		for i, index := range lane {
			rotated[index] = posts[lane[(i+offset)%len(lane)]]
		}
	}
	return rotated
}

// assessAutopilotPublicPosts records completed public analyses. A video
// remains unknown unless both its storyboard and full classification succeed.
func (a *App) assessAutopilotPublicPosts(ctx context.Context, job *autopilotJob, source string, posts []jx.M) error {
	u := a.User(ctx, "", nil)
	var items []jx.M
	var texts []string
	var deferredStreamError error
	imageAttempts := 0
	videoAttempts := 0
	var passes int
	if len(posts) > 1 {
		// Collection passes are persisted before assessment and deduplicated
		// by collection slot. Progress survives restarts without increasing
		// the existing model/network attempt budget.
		if err := a.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM source_autopilot_collection_passes WHERE scope=$1 AND topic_key=$2 AND source_id=$3`, job.Scope, job.TopicKey, source).Scan(&passes); err != nil {
			return err
		}
	}
	for _, post := range autopilotRotateMediaReview(posts, max(0, passes-1)) {
		entry := autopilotPostAuditEntry(ctx, post)
		if !autopilotPublicPostURL(jx.Str(post, "url"), false) {
			entry["outcome"] = "unsafe_or_missing_post_url"
			continue
		}
		if jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") || len(jx.ToStrs(post["safety"])) > 0 || a.ContentIsAdult(post) {
			entry["outcome"] = "public_content_excluded"
			continue
		}
		if textArticle := autopilotTextArticleAlternative(source, post, u.TopicsAcceptText([]string{job.Topic})); textArticle != nil {
			post = textArticle
			entry["representation"] = "article_text_without_unverified_media"
		}
		if PostItemHasVideo(post) {
			entry["outcome"] = "video_unavailable_or_deferred"
			_, _, tiktokPage := autopilotTikTokVideoParts(jx.Str(post, "video_page"))
			if videoAttempts < 1 && (autopilotPublicPostURL(jx.Str(post, "video"), true) || autopilotYouTubeVideoID(jx.Str(post, "video_page")) != "" || tiktokPage) {
				videoAttempts++
				entry["outcome"] = "video_attempted"
				if err := a.assessAutopilotVideoPost(ctx, job, source, post); err != nil {
					// A missing stream is not a reason to lose the other
					// independently reviewable publications in this feed.
					if autopilotStreamUnavailableReason(err) == "" || ctx.Err()!=nil || autopilotBudgetError(ctx)!=nil {
						return err
					}
					deferredStreamError=err
				}
			}
			continue
		}
		if ContentImageKey(post) != "" {
			entry["outcome"] = "image_unavailable_or_deferred"
			if !autopilotPublicPostURL(ContentImageKey(post), true) && !autopilotPublicPostURL(jx.Str(post,"image_fallback"), true) {
				continue
			}
			if imageAttempts >= 2 {
				continue
			}
			imageAttempts++
			entry["outcome"] = "image_attempted"
			if err := a.assessAutopilotImagePost(ctx, job, source, post); err != nil {
				return err
			}
			continue
		}
		if len(items) >= 6 {
			entry["outcome"] = "text_deferred_batch_limit"
			continue
		}
		text := AnalysisText(post)
		if text == "" {
			entry["outcome"] = "missing_text"
			continue
		}
		postHash := repeatDigest(CandidatePostKey(post))
		var exists, wasEligible bool
		if err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`), COALESCE((SELECT eligible FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`),false)`, job.Scope, job.TopicKey, source, postHash, TopicProfileVersion).Scan(&exists, &wasEligible); err != nil {
			return err
		}
		if exists {
			if !wasEligible {
				entry["outcome"] = "already_assessed"
				entry["cached_eligible"] = wasEligible
				continue
			}
			saved, err := a.autopilotHasSavedResult(ctx, job, source, postHash)
			if err != nil {
				return err
			}
			if saved {
				entry["outcome"] = "already_assessed"
				entry["cached_eligible"] = wasEligible
				continue
			}
		}
		if PostIsAdvertisement(post) || IsSpamContent(CleanPostAttribution(text, post), "") {
			entry["outcome"] = "local_ad_or_spam"
			textItem := jx.Copy(post)
			textItem["text_post"] = true
			fingerprint := CandidateTextKey(textItem)
			if fingerprint != "" {
				fingerprint = repeatDigest(fingerprint)
			}
			e := autopilotPostEvidence{PostHash: postHash, ContentHash: fingerprint, PublishedAt: jx.Int(post, "posted_at"), ObservedAt: time.Now().Unix(), Advertising: true}
			if err := recordAutopilotPostEvidence(ctx, a.DB, job, source, e); err != nil {
				return err
			}
			continue
		}
		entry["outcome"] = "text_model_pending"
		entry["sent_text_runes"] = len([]rune(textAnalysisRecord(text)))
		items = append(items, post)
		texts = append(texts, text)
	}
	if len(items) == 0 {
		return deferredStreamError
	}
	analyses, err := u.AnalyzeTextBatch(texts, job.Topic, EmptyHistory())
	if err != nil {
		return err
	}
	if err = autopilotBudgetError(ctx); err != nil {
		return err
	}
	for i, post := range items {
		if i >= len(analyses) || analyses[i] == nil || jx.Bool(analyses[i], "tags_classification_pending") {
			continue
		}
		analysis := analyses[i]
		ad, ok := analysis["advertising"].(bool)
		if !ok {
			continue
		}
		threshold := u.LLM.ThresholdForModel(jx.Str(analysis, "model"), u.TInt("relevance_threshold"))
		relevant := int(jx.Int(analysis, "relevance")) >= threshold && analysis["topic_match"] != false
		admission := u.contentAdmissionReason(analysis, post, job.Topic, texts[i])
		eligible := admission == ""
		entry := autopilotPostAuditEntry(ctx, post)
		autopilotPostAuditVerdict(entry, analysis, relevant, eligible, ad, threshold, admission)
		textItem := jx.Copy(post)
		textItem["text_post"] = true
		fingerprint := CandidateTextKey(textItem)
		if fingerprint != "" {
			fingerprint = repeatDigest(fingerprint)
		}
		e := autopilotPostEvidence{PostHash: repeatDigest(CandidatePostKey(post)), ContentHash: fingerprint, PublishedAt: jx.Int(post, "posted_at"), ObservedAt: time.Now().Unix(), Relevant: relevant, Eligible: eligible, Advertising: ad || PostIsAdvertisement(post)}
		if eligible {
			if err := a.saveAutopilotContentResult(ctx, job, source, post, analysis); err != nil {
				return err
			}
		}
		if err := recordAutopilotPostEvidence(ctx, a.DB, job, source, e); err != nil {
			return err
		}
	}
	return deferredStreamError
}

func (a *App) assessAutopilotVideoPost(ctx context.Context, job *autopilotJob, source string, post jx.M) error {
	entry := autopilotPostAuditEntry(ctx, post)
	postHash := repeatDigest(CandidatePostKey(post))
	var exists, wasEligible bool
	if err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`), COALESCE((SELECT eligible FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`),false)`, job.Scope, job.TopicKey, source, postHash, TopicProfileVersion).Scan(&exists, &wasEligible); err != nil {
		return err
	}
	if exists {
		if !wasEligible {
			entry["outcome"] = "already_assessed"
			entry["cached_eligible"] = wasEligible
			return nil
		}
		saved, err := a.refreshAutopilotMedia(ctx, job, source, postHash, post, true)
		if err != nil {
			return err
		}
		if saved {
			entry["outcome"] = "already_assessed"
			entry["cached_eligible"] = wasEligible
			return nil
		}
	}
	if !a.HasFFmpeg() {
		return nil
	}
	u := a.User(ctx, "", nil)
	f, err := os.CreateTemp("", "posta-autopilot-video-*")
	if err != nil {
		return err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(path)
		return err
	}
	defer os.Remove(path)
	raw := jx.Str(post, "video")
	loaded := false
	youtubePage := jx.Str(post, "video_page")
	_, _, tiktokPage := autopilotTikTokVideoParts(youtubePage)
	if autopilotYouTubeVideoID(youtubePage) != "" || tiktokPage {
		var err error
		if tiktokPage {
			err = u.autopilotDownloadTikTok(youtubePage, path)
		} else {
			err = u.autopilotDownloadYouTube(youtubePage, path)
		}
		if err != nil {
			if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
				return budgetErr
			}
			if tiktokPage {
				return errAutopilotTikTokStreamUnavailable
			}
			return errAutopilotYouTubeStreamUnavailable
		}
		loaded = true
	} else {
		for hop := 0; hop <= 3; hop++ {
			res, err := u.FetchPublic(raw, FetchOpts{MaxBytes: publicClassificationVideoMaxBytes, Timeout: 90 * time.Second, Dest: path})
			if err != nil {
				return autopilotBudgetError(ctx)
			}
			if res.Status >= 300 && res.Status < 400 && res.Location != "" {
				raw = RSSAbsoluteURL(raw, res.Location)
				continue
			}
			loaded = res.OK
			break
		}
	}
	if !loaded || ctx.Err() != nil {
		return ctx.Err()
	}
	video, err := os.ReadFile(path)
	if err != nil || len(video) == 0 {
		return nil
	}
	if mime := http.DetectContentType(video); mime != "video/mp4" && mime != "video/webm" {
		return nil
	}
	codec := strings.ToLower(a.ffprobeContext(ctx, path, "-select_streams", "v:0", "-show_entries", "stream=codec_name", "-of", "csv=p=0"))
	if !contains([]string{"h264", "vp8", "vp9", "av1"}, codec) {
		return nil
	}
	board := a.videoStoryboardContext(ctx, path, a.TInt("video_frames"))
	if board == nil || len(board.Frames) < 2 || ctx.Err() != nil {
		return ctx.Err()
	}
	entry["outcome"] = "vision_model_pending"
	entry["media_frames_prepared"] = len(board.Frames)
	analysis, caption, err := u.analyzeCandidateVision(board.Frames, post, EmptyHistory(), job.Topic, board)
	if err != nil {
		if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
			return budgetErr
		}
		return err
	}
	if analysis == nil {
		return nil
	}
	u.applyVideoClassification(analysis, post, path, caption, u.analysisPrivate(job.Topic, post))
	if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
		return budgetErr
	}
	if jx.Bool(analysis, "tags_classification_pending") || strings.TrimSpace(jx.Str(analysis, "model")) == "" {
		if u.classificationErr != nil {
			return u.classificationErr
		}
		return nil
	}
	ad, ok := analysis["advertising"].(bool)
	if !ok {
		return nil
	}
	threshold := u.LLM.ThresholdForModel(jx.Str(analysis, "model"), u.TInt("relevance_threshold"))
	relevant := int(jx.Int(analysis, "relevance")) >= threshold && analysis["topic_match"] != false
	admission := u.contentAdmissionReason(analysis, post, job.Topic, AnalysisText(post))
	eligible := admission == ""
	autopilotPostAuditVerdict(entry, analysis, relevant, eligible, ad, threshold, admission)
	digest := sha256.Sum256(video)
	e := autopilotPostEvidence{PostHash: postHash, ContentHash: "video:" + hex.EncodeToString(digest[:]), PublishedAt: jx.Int(post, "posted_at"), ObservedAt: time.Now().Unix(), Relevant: relevant, Eligible: eligible, Advertising: ad || PostIsAdvertisement(post)}
	if !eligible {
		return recordAutopilotPostEvidence(ctx, a.DB, job, source, e)
	}
	verifiedPost := jx.Copy(post)
	delete(verifiedPost, "gallery")
	delete(verifiedPost, "images")
	delete(verifiedPost, "media")
	if autopilotYouTubeVideoID(youtubePage) == "" && !tiktokPage {
		delete(verifiedPost, "video_page")
	}
	delete(verifiedPost, "video_via")
	delete(verifiedPost, "image_local")
	delete(verifiedPost, "video_local")
	delete(verifiedPost, "phash")
	// A separate remote poster may change independently of the assessed video.
	delete(verifiedPost, "image")
	verifiedPost["repeat_file_hashes"] = []any{hex.EncodeToString(digest[:])}
	local, err := a.cacheAutopilotMedia(ctx, job, source, hex.EncodeToString(digest[:]), video, true)
	if err != nil {
		return err
	}
	if job.Scope == autopilotAutoScope {
		if local == "" {
			return nil
		}
		verifiedPost["video_local"] = local
		if autopilotYouTubeVideoID(youtubePage) != "" || tiktokPage {
			verifiedPost["video"] = local
		}
		posterHash := sha256.Sum256(board.Frames[0])
		poster, err := a.cacheAutopilotMedia(ctx, job, source, hex.EncodeToString(posterHash[:]), board.Frames[0], false)
		if err != nil {
			return err
		}
		if poster == "" {
			return nil
		}
		verifiedPost["image"], verifiedPost["image_local"] = poster, poster
	}
	if err := a.saveAutopilotContentResult(ctx, job, source, verifiedPost, analysis); err != nil {
		return err
	}
	return recordAutopilotPostEvidence(ctx, a.DB, job, source, e)
}

func (a *App) assessAutopilotImagePost(ctx context.Context, job *autopilotJob, source string, post jx.M) error {
	entry := autopilotPostAuditEntry(ctx, post)
	postHash := repeatDigest(CandidatePostKey(post))
	var exists, wasEligible bool
	if err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`), COALESCE((SELECT eligible FROM source_autopilot_post_evidence WHERE scope=$1 AND topic_key=$2 AND source_id=$3 AND post_hash=$4 AND policy_version=$5 AND observed_at >= `+autopilotFullRecheckEpochSQL+`),false)`, job.Scope, job.TopicKey, source, postHash, TopicProfileVersion).Scan(&exists, &wasEligible); err != nil {
		return err
	}
	if exists {
		if !wasEligible {
			entry["outcome"] = "already_assessed"
			entry["cached_eligible"] = wasEligible
			return nil
		}
		saved, err := a.refreshAutopilotMedia(ctx, job, source, postHash, post, false)
		if err != nil {
			return err
		}
		if saved {
			entry["outcome"] = "already_assessed"
			entry["cached_eligible"] = wasEligible
			return nil
		}
	}
	u := a.User(ctx, "", nil)
	image, resolvedImage, err := u.autopilotReadPostImage(post)
	if err != nil {
		return autopilotBudgetError(ctx)
	}
	entry["outcome"] = "vision_model_pending"
	entry["media_frames_prepared"] = 1
	analysis, _, err := u.analyzeCandidateVision([][]byte{image}, post, EmptyHistory(), job.Topic, nil)
	if err != nil {
		if budgetErr := autopilotBudgetError(ctx); budgetErr != nil {
			return budgetErr
		}
		return err
	}
	if analysis == nil || jx.Bool(analysis, "tags_classification_pending") {
		if u.classificationErr != nil {
			return u.classificationErr
		}
		return nil
	}
	ad, ok := analysis["advertising"].(bool)
	if !ok {
		return nil
	}
	threshold := u.LLM.ThresholdForModel(jx.Str(analysis, "model"), u.TInt("relevance_threshold"))
	relevant := int(jx.Int(analysis, "relevance")) >= threshold && analysis["topic_match"] != false
	text := AnalysisText(post)
	admission := u.contentAdmissionReason(analysis, post, job.Topic, text)
	eligible := admission == ""
	autopilotPostAuditVerdict(entry, analysis, relevant, eligible, ad, threshold, admission)
	digest := sha256.Sum256(image)
	e := autopilotPostEvidence{PostHash: postHash, ContentHash: "image:" + hex.EncodeToString(digest[:]), PublishedAt: jx.Int(post, "posted_at"), ObservedAt: time.Now().Unix(), Relevant: relevant, Eligible: eligible, Advertising: ad || PostIsAdvertisement(post)}
	if !eligible {
		return recordAutopilotPostEvidence(ctx, a.DB, job, source, e)
	}
	verifiedPost := jx.Copy(post)
	delete(verifiedPost, "gallery")
	delete(verifiedPost, "images")
	delete(verifiedPost, "media")
	delete(verifiedPost, "image_local")
	delete(verifiedPost, "video_local")
	delete(verifiedPost, "image_fallback")
	verifiedPost["image"] = resolvedImage
	verifiedPost["phash"] = ImagePhash(image)
	verifiedPost["repeat_file_hashes"] = []any{hex.EncodeToString(digest[:])}
	local, err := a.cacheAutopilotMedia(ctx, job, source, hex.EncodeToString(digest[:]), image, false)
	if err != nil {
		return err
	}
	if job.Scope == autopilotAutoScope {
		if local == "" {
			return nil
		}
		verifiedPost["image_local"] = local
	}
	if err := a.saveAutopilotContentResult(ctx, job, source, verifiedPost, analysis); err != nil {
		return err
	}
	return recordAutopilotPostEvidence(ctx, a.DB, job, source, e)
}
