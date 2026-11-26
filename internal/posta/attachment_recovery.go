package posta

import (
	"path/filepath"
	"strings"
	"time"

	"posta/internal/jx"
)

// The normal collector shares the existing persistent download queue with
// source previews. A deferred post is retried on a later collection pass,
// without a negative model verdict or a second download pipeline.
type attachmentRecovery struct {
	u *U
	left int
	states map[string]jx.M
	telegramChecked, telegramConnected bool
}

func newAttachmentRecovery(u *U) *attachmentRecovery {
	return &attachmentRecovery{u: u, left: 4, states: map[string]jx.M{}}
}

func (r *attachmentRecovery) resolveVideo(page string) string {
	via := VideoPageVia(page)
	if via == "" || r.u.ID == "" || r.u.Ctx.Err() != nil {
		return ""
	}
	if st, ok := r.states[page]; ok {
		return jx.Str(st, "local")
	}
	st := r.u.VideoPreviewStatus(r.u.Ctx, page)
	if jx.Str(st, "status") == "done" {
		r.states[page] = st
		return jx.Str(st, "local")
	}
	r.states[page] = jx.M{}
	if r.left <= 0 || (jx.Str(st, "status") == "failed" && now()-jx.Int(st, "at") < 3600) {
		return ""
	}
	if via == "tgu" {
		if !r.telegramChecked {
			r.telegramChecked = true
			r.telegramConnected = r.u.TelegramUserConnected()
		}
		if !r.telegramConnected {
			return ""
		}
		if until, ok := telegramHistoryPause.Load(r.u.ID); ok && time.Now().Before(until.(time.Time)) {
			return ""
		}
	}
	if jx.Str(st, "status") == "queued" || jx.Str(st, "status") == "running" {
		return ""
	}
	r.left--
	// Never evict an interactive preview or move background work ahead of it.
	r.u.VideoFetchEnqueue(r.u.Ctx, page, via, r.u.ID, false)
	return ""
}

func (r *attachmentRecovery) post(post jx.M) jx.M {
	if r.u.ID == "" || r.u.DB == nil {
		return post
	}
	return restoreAttachmentInventory(post, r.resolveVideo, r.u.MediaExisting, func(video string) string {
		path := r.u.MediaPath(video)
		if path == "" || r.u.MediaExisting(video) == "" || r.left <= 0 {
			return ""
		}
		r.left--
		board := r.u.videoStoryboardContext(r.u.Ctx, path, 1)
		if board == nil || len(board.Frames) == 0 {
			return ""
		}
		local := r.u.MediaStoreImage("attachment-poster:"+video, board.Frames[0])
		if local != "" {
			mediaProtect.Store(filepath.Base(local), time.Now())
		}
		return local
	})
}

// Rebuild the gallery only from a known inventory, never from covers or the
// album's outer URL. No mutation of the shared/public observation.
func restoreAttachmentInventory(post jx.M, resolveVideo, existing, poster func(string) string) jx.M {
	inventory := jx.Maps(post, "source_attachments")
	if len(inventory) == 0 || jx.Bool(post, "attachment_inventory_incomplete") || jx.Bool(post, "text_incomplete") {
		return post
	}
	var gallery, attachments []jx.M
	var images []string
	hasVideo, recovered := false, false
	for _, original := range inventory {
		a := jx.Copy(original)
		image, video := jx.Str(a, "image"), jx.Str(a, "video")
		imageLocal, videoLocal := existing(jx.Str(a, "image_local")), existing(jx.Str(a, "video_local"))
		if imageLocal != "" {
			image = imageLocal
		} else if !strings.HasPrefix(image, "https://") {
			image = ""
		}
		switch jx.Str(a, "kind") {
		case "photo":
			if image == "" {
				return post
			}
		case "video":
			hasVideo = true
			if videoLocal != "" {
				video = videoLocal
			} else if !strings.HasPrefix(video, "https://") {
				video = resolveVideo(jx.Str(a, "video_page"))
				videoLocal = existing(video)
				if videoLocal == "" {
					video = ""
				} else {
					recovered = true
				}
			}
			if video == "" {
				// Continue on later passes, scheduling each missing attachment
				// independently. Never substitute a still image for a video.
				return post
			}
			if image == "" && videoLocal != "" {
				imageLocal = poster(videoLocal)
				image = imageLocal
			}
		default:
			return post
		}
		if image != "" {
			images = append(images, image)
		}
		a["image"], a["video"], a["image_local"], a["video_local"] = image, video, imageLocal, videoLocal
		gallery = append(gallery, jx.M{"image": image, "video": video, "image_local": imageLocal, "video_local": videoLocal})
		attachments = append(attachments, a)
	}
	if !hasVideo {
		return post // existing photo-only pipeline
	}
	if len(images) == 0 && (len(gallery) > 1 || !strings.HasPrefix(jx.Str(gallery[0], "video"), "https://")) {
		return post
	}
	out := jx.Copy(post)
	main := gallery[0]
	for _, f := range gallery {
		if jx.Str(f, "image") != "" {
			main = f
			break
		}
	}
	out["image"], out["video"], out["image_local"], out["video_local"], out["images"] = main["image"], main["video"], main["image_local"], main["video_local"], images
	out["gallery"] = []jx.M{}
	if len(gallery) > 1 {
		out["gallery"] = gallery
	}
	out["source_attachments"] = attachments
	out["media_incomplete"], out["video_unavailable"], out["text_post"] = false, false, false
	// The top-level pending job describes a single older attachment. All
	// actual videos are now represented by their own gallery records.
	delete(out, "video_via")
	delete(out, "video_page")
	if recovered {
		out["attachment_recovered"] = true
	}
	return out
}
