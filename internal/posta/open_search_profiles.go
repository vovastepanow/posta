package posta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"posta/internal/config"
)

// Preserve punctuation and all explicit qualifications in search intent.
// In particular C++, C#, and C are different requests, not aliases.
func openSearchProfileKey(query string) string {
	return literalIntentKey(NormalizeTopic(query))
}

func openSearchProfileID(query string) string {
	sum := sha256.Sum256([]byte("open-search-profile:" + openSearchProfileKey(query)))
	return "t" + hex.EncodeToString(sum[:4])
}

func (a *App) openProfilesEnabled() bool {
	return a != nil && a.DB != nil && a.TBool("public_open_search")
}

func (a *App) exactSearchProfile(query string) *Topic {
	if a == nil || a.catalog == nil {
		return nil
	}
	key := openSearchProfileKey(query)
	if key == "" {
		return nil
	}
	for _, entry := range a.TopicRegistry() {
		if openSearchProfileKey(entry.Title) == key {
			return entry
		}
	}
	return nil
}

func (a *App) ensureOpenSearchProfile(ctx context.Context, query string) (*Topic, error) {
	query = NormalizeTopic(query)
	if query == "" {
		return nil, Bad("Пустой поисковый запрос.")
	}
	if e := a.exactSearchProfile(query); e != nil {
		return e, nil
	}
	return a.TopicSave(ctx, &Topic{
		ID: openSearchProfileID(query), Title: query, Scope: "narrow",
		Classified: true, Smart: true, Manual: true,
	})
}

// Full compound intent keys must not truncate to the primary topic length.
func literalIntentKey(query string) string {
	return strings.ToLower(collapseSpaces(query))
}

func NormalizeOpenQuery(query string) string {
	return trimRunes(collapseSpaces(query), config.TopicQueryMaxLen)
}
