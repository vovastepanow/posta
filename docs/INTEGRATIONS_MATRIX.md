# Integration matrix and refactoring scope

Baseline: 46921ea83903. This is a static code audit, not live API certification.

## Publishing

Separate the supported product scope from executable adapter and account-specific readiness. The priority scope is Telegram, VK, OK, MAX, Instagram, Facebook, YouTube, TikTok, Rutube, Threads, X and Pinterest. The new-connection catalog exposes only priority adapters with actual connection and publish handlers and ready/beta status. Beta remains owner-only. Telegram has its own connection flow. Existing resources, queued deliveries and non-priority adapters are retained.

| Platform | Existing implementation / gap |
|---|---|
| Telegram | Bot and MTProto scheduling; verify album and long-caption handling |
| VK | Token connection, wall publishing, photos; video not supported in current registry |
| OK | Requires app key, text publishing only |
| MAX | Bot, text only |
| YouTube | OAuth, video upload; app keys/audit required |
| Pinterest | OAuth, photo pin; app keys required |
| X | OAuth, text publishing; app keys and API access required |
| Instagram, Facebook, Threads | Placeholder, not an executable publisher |
| TikTok, Rutube | No executable publisher in current registry |
| Discord, Mastodon, Bluesky, Reddit, Tumblr, LinkedIn | Retain existing code and resources; defer new connections |
| Dzen, Viber | No executable direct publisher |

Limits in third-party proposals are not specifications until checked against the actual API, permission level and account type.

## Ingestion

| Area | Existing code | Next audit |
|---|---|---|
| Telegram | sources.go, telegram_user.go, internal/tgclient | public preview parses posts, MTProto downloads specific video; complete history and album reading still need verification |
| VK | sources.go, social_public.go, shared_collection.go | API and public fallback already exist; check completeness and typed outcomes |
| RSS and sites | rss.go, article.go, article_resolve.go, source_autopilot_feed_reader.go | reuse existing article extractor, distinguish partial/restricted/full |
| OK/TikTok | ok_tiktok.go | OK drops image-less items and truncates text; TikTok video metadata and expiry |
| YouTube/Rutube | youtube.go, ytdlp.go, source_autopilot_youtube*.go | metadata fallback, API quota and playable media |
| Reddit/Discord | reddit_source.go, discord_source.go | access scope, gallery and attachment expiry |
| Meta/Threads/X/Dzen | meta_sources.go, threads_source.go, x_source.go, dzen_source.go | account permissions, carousel, full text and schema changes |
| Other source adapters | source_autopilot_*.go, social_public.go, imageboard.go | enumerate actual support and distinguish live checks from fixture coverage |

## Design constraints

- Reuse existing collection, media, article and outbox mechanisms; avoid parallel pipelines and a big-bang database migration.
- Separate fetch outcome from content relevance, and confirmed empty feed from an unknown result.
- Scope authenticated reads and media caches to account/access rights. Never try to bypass private-source restrictions.
- Keep per-field provenance and completeness (unknown, partial, complete, unavailable); do not replace a rich record with a poorer fallback.
- Partial evidence must not penalize sources or be treated as full multimodal evidence. A permanently unavailable attachment must not block an entire queue.
- Publish variants are immutable derivatives. Unsupported transformations must be explicit; ambiguous send outcomes require reconciliation, not blind retry or a claim that absent recent results proves non-delivery.
- Separate deterministic recorded fixtures, live read-only smoke tests, and explicitly authorized test-account publishing. No production publishing during CI.
- First end-to-end pair: Telegram and VK, then RSS/article as an independent intake path. Start with existing fixtures and add independently verified real posts; avoid claiming live success based on synthetic tests.
