# Integration matrix and refactoring scope

Baseline: 46921ea83903. This is a static code audit, not live API certification.

## Publishing

The product scope is exactly SmmBox's 14 destinations: Telegram, VK, OK, MAX, Instagram, Facebook, YouTube, TikTok, Rutube, Threads, X, Pinterest, Tumblr and Dzen. New connections require both inclusion in scope and an implemented ready/beta adapter. Existing resources and queued deliveries are retained; out-of-scope adapters remain dormant for new connections. Beta remains owner-only.

Source evidence (checked 2026-11-26): https://smmbox.com/en/tarif.php and https://smmbox.com/ . Competitor claims are scope evidence, not evidence that Posta has working APIs.

| Platform | Existing implementation / gap |
|---|---|
| Telegram | Bot and MTProto scheduling; verify album and long-caption handling |
| VK | Token connection, wall publishing, photos; video not supported in current registry |
| OK | Requires app key, text publishing only |
| MAX | Bot, text and uploaded image/video attachments; explicit attachment-processing retry; live account test pending |
| YouTube | OAuth, video upload; app keys/audit required |
| Pinterest | OAuth, photo pin; app keys required |
| X | OAuth, text publishing; app keys and API access required |
| Instagram, Facebook, Threads | Placeholder, not an executable publisher |
| TikTok, Rutube | No executable publisher in current registry |
| Tumblr | In scope; existing text adapter requires app keys |
| Discord, Mastodon, Bluesky, Reddit, LinkedIn | Existing adapters preserved, new connections disabled |
| WordPress | Out of scope; existing text adapter preserved |
| TenChat, Medium, Bitrix24, Google Business Profile | Out of scope |
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

## Acquisition scope

Never restrict intake to publishing destinations. Keep the existing social, feed, news, forum, video, imageboard and federated sources. Expand incrementally to useful public sources (e.g. PeerTube, podcast RSS, public newsletters and blogs, TenChat, public WordPress sites, public GitHub release feeds, public discussion communities) with explicit provenance and permissions. A candidate is not a verified adapter. No private content or circumvention of access controls. No shared cache for private/account-scoped content.

WordPress REST API reference: https://developer.wordpress.org/rest-api/reference/posts/ ; https://developer.wordpress.org/rest-api/using-the-rest-api/authentication/ ; https://developer.wordpress.org/rest-api/reference/users/ .

## Current reliability work

- Publication success requires a remote identifier. Transport failures, ambiguous receipts and server failures stop automatic retry when identified as uncertain; existing failed-state UI asks the user to reconcile before manual retry. A full provider-specific reconciliation protocol is not implemented yet.
- Ordinary RSS polling reuses same-origin alternate discovery and bounded size retry. Invalid/truncated XML and partial HTTP responses cannot count as a successful empty feed.
- Explicitly partial public refreshes keep one previous unflagged snapshot in the JSON record without merging attachments across edits. The latest incomplete observation is not sent through global public topic classification. This is not a full media repair queue or proof of completeness of the previous observation.

MAX media API reference (checked 2026-11-26): https://dev.max.ru/docs-api/use-cases/sending-messages/media/ and https://dev.max.ru/docs-api/methods/POST/uploads . The provider supports up to 12 attachments; the existing editor has its own 10-file cap. Local preflight and fixture tests do not certify credentials or live delivery.
