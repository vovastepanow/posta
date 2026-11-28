package posta

import (
	"context"

	"github.com/jackc/pgx/v5"
	"posta/internal/jx"
)

// Import independently assessed public posts into the shared search inventory.
// Do not promote the source, copy its topic verdict or overwrite a newer feed
// observation (including an incomplete or sensitive revision).
func storeAssessedPublicPost(ctx context.Context, tx pgx.Tx, source string, post jx.M) error {
	platform, _, _, ok := autopilotSourceParts(source)
	if !ok || !autopilotPostMatchesSource(source, post) ||
		!autopilotPublicPostURL(jx.Str(post, "url"), false) ||
		sourceReviewSensitive(post) || jx.Bool(post, "text_incomplete") ||
		jx.Bool(post, "media_incomplete") || jx.Bool(post, "video_unavailable") ||
		PostIsAdvertisement(post) || PostSearchIsAd(post) {
		return nil
	}
	key := CandidatePostKey(post)
	if key == "" {
		return nil
	}
	// Copy only publicly collected content, not the profile-specific analysis.
	raw := jx.Copy(post)
	raw["source_key"] = source
	_, err := tx.Exec(ctx, `INSERT INTO public_posts(source_key,post_key,platform,data,first_seen,last_seen)
		VALUES($1,$2,$3,$4,$5,$5) ON CONFLICT(source_key,post_key) DO NOTHING`,
		source, key, platform, jx.Marshal(raw), now())
	return err
}
