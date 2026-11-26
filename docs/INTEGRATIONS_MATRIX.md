# Integration matrix and refactoring scope

Baseline: 46921ea83903. Updated publishing implementation: 83b51dd4c630. This is a static code audit plus deterministic/CI tests, not live API certification.

## Publishing

The product scope is exactly SmmBox's 14 destinations: Telegram, VK, OK, MAX, Instagram, Facebook, YouTube, TikTok, Rutube, Threads, X, Pinterest, Tumblr and Dzen. New connections require both inclusion in scope and an implemented ready/beta adapter. Existing resources and queued deliveries are retained; out-of-scope adapters remain dormant for new connections. Beta remains owner-only.

Source evidence (checked 2026-11-26): https://smmbox.com/en/tarif.php and https://smmbox.com/ . Competitor claims are scope evidence, not evidence that Posta has working APIs.

| Platform | Existing implementation / gap |
|---|---|
| Telegram | Bot and MTProto scheduling; verify album and long-caption handling |
| VK | Wall text/photo/video publishing, validated upload destination and group ownership; video.save requires authorized user token; live account test pending |
| OK | Group text, photos and videos; requires app key and relevant GROUP_CONTENT, PHOTO_CONTENT, VIDEO_CONTENT and VALUABLE_ACCESS permissions; live account test pending |
| MAX | Bot, text and uploaded image/video attachments; explicit attachment-processing retry; live account test pending |
| YouTube | OAuth, resumable video upload in 8 MiB chunks, server-confirmed range recovery and validated video receipt; app keys/audit required |
| Pinterest | OAuth, single photo or video pin, generated video cover, media processing status; app keys and live account test required; first selected board retained |
| X | OAuth text/photos, single GIF or chunked video; media.write scope and API access required, live account test pending |
| Facebook | Owner beta, explicit Page token and ID, text/photo feed publishing or one native MP4 video; app permissions and real account test required |
| Instagram | Professional account OAuth (Instagram Login), JPEG, Reels and ordered carousels; HTTPS media pull and app approval required; live account check pending |
| Threads | OAuth, text, JPEG/photo, video and ordered carousels; HTTPS media pull and app approval required; live account check pending |
| TikTok, Rutube | No executable publisher in current registry |
| Tumblr | OAuth NPF text/photos/GIF and one uploaded video; asynchronous video processing, live account check pending |
| Discord, Mastodon, Bluesky, Reddit, LinkedIn | Existing adapters preserved, new connections disabled |
| WordPress | Out of scope; existing text adapter preserved |
| TenChat, Medium, Bitrix24, Google Business Profile | Out of scope |
| Dzen, Viber | No executable direct publisher; Dzen Telegram synchronization is external to Posta |

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

OK references: https://apiok.ru/dev/examples/photo_upload ; https://apiok.ru/dev/methods/rest/photosV2/photosV2.getUploadUrl ; https://apiok.ru/dev/methods/rest/mediatopic/mediatopic.post ; https://new.apiok.ru/dev/methods/ . Group topic photographs use the uploaded tokens directly without a separate commit. The 10-item / 20 MB image and 200 MB video limits here are conservative local limits, not certified provider maxima. Video workflow references: https://apiok.ru/dev/examples/video_upload ; https://apiok.ru/dev/methods/rest/video/video.getUploadUrl ; https://apiok.ru/dev/methods/rest/video/video.update . Upload with post_form=true; finalize only after all attachments upload; after video.update, uncertain outcomes stop automatic replay.

Tumblr references: https://github.com/tumblr/docs/blob/master/api.md and https://github.com/tumblr/docs/blob/master/npf-spec.md ; https://help.tumblr.com/knowledge-base/posting-video/ . NPF allows at most one native uploaded video per post. A creation receipt confirms acceptance, not completion of remote video processing. The existing editor limits the album to 10 files; the API supports more image blocks.

Facebook Pages API references: https://developers.facebook.com/docs/graph-api/reference/page/photos/ and https://developers.facebook.com/docs/graph-api/reference/page/feed/ . Connection validates the explicit Page identity; successful identity lookup alone does not establish publishing permission. Existing global read-only tokens are never reused to publish on behalf of an account.

Meta container publishing: https://www.postman.com/meta/threads/documentation/dht3nzz/threads-api ; https://www.postman.com/meta/instagram/documentation/6yqw8pt/instagram-api . Publishing uses account-scoped tokens, never global read tokens. Media is exposed only via revocable 15-minute bearer links, not arbitrary file paths. Instagram photo variants are JPEG without automatic crop; unsupported aspect ratios are explicitly rejected. Shared local input limit is 200 MiB and editor max is 10 attachments. Token refresh occurs on delivery within seven days of expiry; accounts idle beyond token expiry require reauthorization. No live publish is performed in tests.

X media API: https://docs.x.com/x-api/media/quickstart/media-upload-chunked and https://docs.x.com/x-api/media/upload-media . Uses dedicated v2 initialize/append/finalize endpoints and checks processing state before publishing. Old account grants may need reconnection to grant media.write. Local limit is deliberately smaller than the provider maximum.

YouTube resumable protocol: https://developers.google.com/youtube/data/guides/using_resumable_upload_protocol . A transfer uses one upload session; uncertain replies are probed in that session, and unresolved results require reconciliation. This is not persisted cross-restart resume.

Pinterest video pin references: https://developers.pinterest.com/docs/work-with-organic-content-and-users/create-boards-and-pins/ ; https://developer.pinterest.com/docs/api/v5/media-create/ ; https://github.com/pinterest/pinterest-python-generated-api-client/blob/main/docs/PinMediaSourceVideoID.md . Upload uses a validated Pinterest S3 host without OAuth bearer, waits for succeeded processing, and creates a cover from a local video frame. Existing OAuth grants may require reconnection. The current resource selects a board during connection; a full board picker is not yet implemented.

OAuth callback state is atomically consumed once, tied to the initiating signed-in Posta profile, with owner/beta permission rechecked before connection. CI includes a concurrent PostgreSQL replay/isolation test. This does not certify an actual provider consent flow.

Remaining product-scope blockers: TikTok app approval and mandatory user-facing posting controls; Rutube confirmed publishing API/partner access; Dzen direct publisher access. Do not expose placeholders as operational or bypass account restrictions. VK video and Facebook video still need verified provider workflows and authorized tests.

## Feed ingestion changes (2026-11-26)

- JSON Feed 1.0 and 1.1 are normalized into the existing RSS/Atom tree (https://www.jsonfeed.org/version/1.1/). Discovery, title extraction, review and collection share the parser. Plain-text content remains literal, stable entry IDs and authors are retained; malformed envelopes and duplicate IDs are not accepted as empty feeds. Supported media remain photo and direct MP4/WebM; podcast/audio ingestion is not implemented.
- Ordered enclosures preserve mixed albums and multiple video files. Media RSS groups are alternative renditions (https://www.rssboard.org/media-rss), not duplicate album items; selected renditions retain their own thumbnail. Incomplete unsupported video is not classified as text-only.
- Public YouTube feeds are admitted to shared collection only in their existing validated canonical form. User RSS fetches respect cancellation.
- Rutube retains API descriptions and actual publication time; channel collection reuses the existing author attribution check. Paid/locked/deleted/hidden/live/audio-only records are excluded from the video ingestion route. Archived search responses are not live certification.
- Separate live feed smoke tests only read fixed public feed URLs and report format/counts. They do not publish, write production records, invoke AI, or certify every provider.

VK video upload: https://github.com/VKCOM/vk-api-schema/blob/master/video/methods.json and https://github.com/VKCOM/vk-php-sdk#53-uploading-video-files . video.save wallpost=0, followed by multipart video_file upload; confirm video ID and attach to wall post. A successful upload is not proof that remote video transcoding has finished. Partial video publication or an ambiguous upload stops automatic replay. No VK Clips/Stories side effect or undocumented token acquisition.

Facebook video publishing: https://developers.facebook.com/docs/graph-api/reference/page/videos/ . A page access token and documented content permissions are required. One MP4 (local limit 200 MB) uses the graph-video host with multipart source and description. Mixed photo/video or multiple video posts are rejected before upload. A returned video ID confirms API acceptance, not completed transcoding or public visibility. Live test pending.

The connection catalog exposes the 14-product scope (Telegram separately); unsupported destinations and missing keys are visible as unavailable, and beta connection buttons are only exposed to the owner. Existing resources remain visible.

## Post editor and additions

The composer now checks the intersection of implemented capabilities for selected destinations. A conflicting selection is not silently stripped of attachments. Telegram audio (MP3/M4A) and PDF use the bot/Posta queue, with homogeneous audio/document albums only; account-native scheduling remains unsupported for these formats. The input is bounded to 50 MB per audio/document file; audio requires ffprobe. Telegram, OK and X support native polls through their respective adapters. X uses single choice, 2–4 answers up to 25 characters and a 5–10080 minute duration; other poll limits are conservative local limits. VK polls use polls.create with an identified channel user grant or the profile's own VK OAuth token, followed by wall.post with the verified community-owned poll attachment. A community token alone does not enable the control. Token presence does not guarantee method permission; live account certification is required. Existing manually entered user grants without type metadata may need reconnection. Live account certification remains outstanding.

References: https://core.telegram.org/bots/api#sendaudio ; https://core.telegram.org/bots/api#sendmediagroup ; https://core.telegram.org/bots/api#sendpoll ; https://apiok.ru/dev/methods/rest/mediatopic/mediatopic.post ; https://docs.x.com/x-api/posts/create-post ; https://help.x.com/en/using-x/x-polls ; https://github.com/VKCOM/vk-api-schema/blob/master/polls/methods.json .

Telegram: native voice notes use OGG/Opus, one per message; common document formats PDF/TXT/CSV/ZIP/DOCX/XLSX/PPTX are accepted by a bounded allowlist, not arbitrary executable files. MAX now has native audio and file uploads with separate exact upload hosts, provider tokens and receipt validation. Conservatively limit audio and documents to one media attachment on MAX; audio duration must be known and at most 60 minutes. See https://dev.max.ru/docs-api/use-cases/sending-messages/media/ and https://core.telegram.org/bots/api#sendvoice .

Post extras: URL buttons (Telegram single-message posts and MAX), Telegram silent delivery, content protection and media spoilers, explicit text link-preview control on Telegram/MAX. Extras travel through immediate delivery, persisted queue, and queued-post editing; unknown/incompatible extras are rejected. Native MTProto scheduling is intentionally unavailable for these new settings. MAX notifications in channels cannot be disabled according to its API. No callback buttons are exposed without a callback-processing workflow. References: https://core.telegram.org/bots/api ; https://dev.max.ru/docs-api/use-cases/sending-messages/keyboard ; https://dev.max.ru/docs-api/methods/POST/messages/ .

Poll settings: Telegram quiz with one correct option and optional explanation; Telegram and VK close interval; VK/OK public voting. X retains its mandatory single-choice duration. These settings are validated per resource and persisted in the queue. Telegram quiz uses current Bot API correct_option_ids; older self-hosted Bot API servers may require upgrading. References: https://core.telegram.org/bots/api#sendpoll ; https://github.com/VKCOM/vk-api-schema/blob/master/polls/methods.json ; https://apiok.ru/dev/methods/rest/mediatopic/mediatopic.post .

Standalone location and contact cards now use Telegram sendLocation/sendContact and MAX native attachment objects. Coordinates are manually supplied and range-checked; contact names/phone are entered by the user, with escaped vCard serialization for MAX. They are separate posts (not silently appended as extra messages) and are persisted and editable in the queue. See https://core.telegram.org/bots/api#sendlocation and https://dev.max.ru/docs-api/use-cases/sending-messages/another-attachments/ .

Threads text polls use documented poll_attachment (option_a through option_d, 2–4 choices of up to 25 characters) and the existing container/publish path. VK supports uploaded GIF and common documents via docs.getWallUploadServer, multipart file, docs.save and wall.post; document receipt ownership must match the selected community, and uncertain saves stop automatic replay. See https://www.postman.com/meta/threads/documentation/dht3nzz/threads-api and https://github.com/VKCOM/vk-api-schema/blob/master/docs/methods.json .
