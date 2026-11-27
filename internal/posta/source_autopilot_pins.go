package posta

import (
	"github.com/jackc/pgx/v5"
	"posta/internal/db"
	"posta/internal/jx"
)

// Personal pins retain a public RSS entry for this user even if the public
// autopilot subsequently rejects it. They never change the shared catalog.
func (u *U) personalSourcePins(topic string) jx.M {
	return jx.Map(jx.Map(u.Settings(), "source_personal_pins"), autopilotTopicKey(topic))
}

func autopilotPinnablePublicRSS(entry jx.M) bool {
	if sharedSourceEligible(PlatformRSS, entry) {
		return true
	}
	if _, ok := canonicalYouTubeFeed(jx.Str(entry, "id")); ok {
		return jx.Str(entry, "query") == "" && !IsSourceFrozen(entry, 0)
	}
	return jx.Str(entry, "query") == "" && !IsSourceFrozen(entry, 0) && autopilotPublicRedditFeed(jx.Str(entry, "id"))
}

func (u *U) SourcePersonalPin(sid string, pinned bool) error {
	topic := u.CurrentTopic()
	platform, _, kind, ok := autopilotSourceParts(sid)
	if topic == "" || !ok {
		return Bad("Закрепить можно публичный источник текущей темы.")
	}
	key := autopilotTopicKey(topic)
	var found jx.M
	if pinned {
		for _, entry := range *u.catalogWithPersonalPins(topic).List(kind) {
			eligible := sharedSourceEligible(platform, entry) || (platform == PlatformRSS && autopilotPinnablePublicRSS(entry))
			if SourceIDFor(platform, jx.Str(entry, "id")) == sid && eligible && u.CatalogEntryMatchesTopic(entry, topic, false) {
				found = jx.Unmarshal(jx.Marshal(entry))
				break
			}
		}
		if found == nil {
			return NotFound("Публичный источник этой темы не найден.")
		}
		// This copy is used only while collecting this exact personal profile.
		found["autopilot_manual"] = true
		found["topics"] = []any{TopicBase(topic)}
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
		all := jx.Map(settings, "source_personal_pins")
		if all == nil {
			all = jx.M{}
		}
		pins := jx.Map(all, key)
		if pins == nil {
			pins = jx.M{}
		}
		if pinned {
			pins[sid] = found
		} else {
			delete(pins, sid)
		}
		all[key] = pins
		settings["source_personal_pins"] = all
		_, err := tx.Exec(u.Ctx, `UPDATE users SET settings=$2 WHERE id=$1`, u.ID, db.JSON(settings))
		return err
	})
}

func (u *U) catalogWithPersonalPins(topic string) *SourceDB {
	return u.catalogWithPins(topic, u.personalSourcePins(topic))
}

// Index only catalog sections containing valid pins and clone the shared
// snapshot only when a pin actually needs to be restored.
func (u *U) catalogWithPins(topic string, pins jx.M) *SourceDB {
	d := u.Catalog()
	result := d
	visibleByKind := map[string]map[string]bool{}
	for sid, value := range pins {
		entry, ok := value.(map[string]any)
		platform, _, kind, valid := autopilotSourceParts(sid)
		if !ok || !valid || (!sharedSourceEligible(platform, entry) && !(platform == PlatformRSS && autopilotPinnablePublicRSS(entry))) || SourceIDFor(platform, jx.Str(entry, "id")) != sid {
			continue
		}
		visible := visibleByKind[kind]
		if visible == nil {
			visible = map[string]bool{}
			for _, existing := range *d.List(kind) {
				if u.CatalogEntryMatchesTopic(existing, topic, false) {
					visible[SourceIDFor(platform, jx.Str(existing, "id"))] = true
				}
			}
			visibleByKind[kind] = visible
		}
		if visible[sid] {
			continue
		}
		if result == d {
			result = d.Clone()
		}
		list := result.List(kind)
		*list = append(*list, jx.Unmarshal(jx.Marshal(entry)))
		visible[sid] = true
	}
	return result
}
