package posta

import (
	"strings"
)

// Auto source IDs use the same keys as the existing catalog and personal
// overrides. URL-based sources retain the stricter RSS validation.
func autopilotSourceParts(source string) (platform, id, kind string, ok bool) {
	switch {
	case strings.HasPrefix(source, "telegram:"):
		id = strings.TrimPrefix(source, "telegram:")
		platform = PlatformTelegram
		ok = IsValidTelegramChannel(id) && id == strings.ToLower(id) && !contains(telegramReservedPaths, id) && !strings.HasSuffix(id, "bot")
	case strings.HasPrefix(source, "vk:"):
		id = strings.TrimPrefix(source, "vk:")
		platform = PlatformVK
		ok = IsValidVKDomain(id) && id == strings.ToLower(id) && !lsVKBadRe.MatchString(id)
	case strings.HasPrefix(source, "ok:"):
		id = strings.TrimPrefix(source, "ok:")
		platform = PlatformOK
		ok = IsValidOKGroup(id)
	case strings.HasPrefix(source, "tiktok:"):
		id = strings.TrimPrefix(source, "tiktok:")
		platform = PlatformTikTok
		ok = IsValidTikTokUser(id) && id == strings.ToLower(id)
	case strings.HasPrefix(source, "rss:"):
		id = strings.TrimPrefix(source, "rss:")
		platform = PlatformRSS
		canonical, valid := autopilotPublicRSSID(id)
		ok = valid && canonical == source && SourceIDFor(platform, id) == source
	default:
		return "", "", "", false
	}
	return platform, id, CatalogKind(platform), ok
}

func autopilotCandidateID(c DiscoveryCandidate) (string, bool) {
	if c.Platform == PlatformRSS {
		id, ok := autopilotPublicRSSID(c.ID)
		if !ok || SourceIDFor(PlatformRSS, strings.TrimPrefix(id, "rss:")) != id {
			return "", false
		}
		return id, true
	}
	if c.Platform != PlatformTelegram && c.Platform != PlatformVK && c.Platform != PlatformOK && c.Platform != PlatformTikTok {
		return "", false
	}
	id := SourceIDFor(c.Platform, strings.TrimPrefix(c.ID, "@"))
	_, _, _, ok := autopilotSourceParts(id)
	return id, ok
}
