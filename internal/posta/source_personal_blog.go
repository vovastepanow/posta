package posta

import (
	"errors"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
	"posta/internal/jx"
)

// A source's format is orthogonal to its topic: an SMM author and an SMM
// agency can cover the same subject. Unknown is intentionally not "no".
const (
	personalBlogAny     = "any"
	personalBlogOnly    = "only"
	personalBlogExclude = "exclude"
	personalBlogYes     = "yes"
	personalBlogNo      = "no"
	personalBlogUnknown = "unknown"
)

func validPersonalBlogMode(mode string) bool {
	return mode == personalBlogAny || mode == personalBlogOnly || mode == personalBlogExclude
}

func personalBlogKind(raw string) string {
	switch raw {
	case personalBlogYes, personalBlogNo:
		return raw
	default:
		return personalBlogUnknown
	}
}

func personalBlogAllowed(mode, kind string) bool {
	switch mode {
	case personalBlogOnly:
		return personalBlogKind(kind) == personalBlogYes
	case personalBlogExclude:
		return personalBlogKind(kind) == personalBlogNo
	default:
		return true
	}
}

// PersonalBlogMode exposes the same per-topic preference to the content tab.
func (u *U) PersonalBlogMode(topic string) string {
	return u.personalBlogMode(topic)
}

// poolPersonalBlogFilter applies the same classification used by collection
// and delivery, including the existing fail-closed handling of unknowns.
func (u *U) poolPersonalBlogFilter(topic string) (func(jx.M) bool, error) {
	mode := u.personalBlogMode(topic)
	if mode == personalBlogAny {
		return func(jx.M) bool { return true }, nil
	}
	kinds, err := u.personalBlogSourceKinds(topic)
	if err != nil {
		return nil, err
	}
	return func(item jx.M) bool {
		return jx.Bool(item, "own") || personalBlogAllowed(mode, kinds[ContentSourceKey(item)])
	}, nil
}

func (u *U) personalBlogMode(topic string) string {
	mode := jx.Str(jx.Map(u.Settings(), "personal_blog_modes"), autopilotTopicKey(topic))
	if !validPersonalBlogMode(mode) {
		return personalBlogAny
	}
	return mode
}

// Settings are per-user and per-topic, without changing public source
// verification for other users subscribed to the same topic.
func (u *U) SavePersonalBlogMode(topic, mode string) error {
	if u.ID == "" || autopilotCanonicalTopic(topic) == "" || !validPersonalBlogMode(mode) {
		return Bad("Некорректный фильтр личных блогов.")
	}
	return u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(u.Ctx, `SELECT settings FROM users WHERE id=$1 FOR UPDATE`, u.ID).Scan(&raw); err != nil {
			return err
		}
		settings := jx.Unmarshal(raw)
		if settings == nil {
			settings = jx.M{}
		}
		modes := jx.Map(settings, "personal_blog_modes")
		if modes == nil {
			modes = jx.M{}
		}
		key := autopilotTopicKey(topic)
		if mode == personalBlogAny {
			delete(modes, key)
		} else {
			modes[key] = mode
		}
		settings["personal_blog_modes"] = modes
		_, err := tx.Exec(u.Ctx, `UPDATE users SET settings=$2 WHERE id=$1`, u.ID, db.JSON(settings))
		return err
	})
}

// Source format is recorded in the normal, publicly reviewed source evidence.
// Both public scopes share a classifier result; neither overrides another
// user's preferences. Never guess "no" for an unclassified source.
func (u *U) personalBlogSourceKinds(topic string) (map[string]string, error) {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT source_id,evidence->>'personal_blog'
		FROM source_autopilot_sources
		WHERE topic_key=$1 AND scope IN ('public','public:shadow')
		ORDER BY CASE scope WHEN 'public' THEN 0 ELSE 1 END`, autopilotTopicKey(topic))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	kinds := map[string]string{}
	for rows.Next() {
		var source string
		var label *string
		if err := rows.Scan(&source, &label); err != nil {
			return nil, err
		}
		if label == nil {
			continue
		}
		if _, exists := kinds[source]; !exists && personalBlogKind(*label) != personalBlogUnknown {
			kinds[source] = personalBlogKind(*label)
		}
	}
	return kinds, rows.Err()
}

func (u *U) personalBlogSourceAllowed(topic, source string) (bool, error) {
	mode := u.personalBlogMode(topic)
	if mode == personalBlogAny {
		return true, nil
	}
	kinds, err := u.personalBlogSourceKinds(topic)
	if err != nil {
		return false, err
	}
	if source == "" {
		return false, errors.New("missing source identity")
	}
	return personalBlogAllowed(mode, kinds[source]), nil
}

// Prevent accidental invalid labels in stored evidence and model responses.
func personalBlogLabelFromReview(raw string) string {
	return personalBlogKind(raw)
}

