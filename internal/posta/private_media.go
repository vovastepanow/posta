package posta

import (
	"context"
	"os"
)

// A file can have several authorized users (legacy shared imports), but
// a missing owner row never grants access when database lookup fails.
func (a *App) CachedMediaAllowed(ctx context.Context, uid, name string) bool {
	if uid == "" || !mediaNameRe.MatchString(name) || a.DB == nil {
		return false
	}
	var restricted, own bool
	err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM private_media WHERE name=$1),
		EXISTS (SELECT 1 FROM private_media WHERE name=$1 AND user_id=$2)`, name, uid).Scan(&restricted, &own)
	return err == nil && (!restricted || own)
}

func (u *U) registerPrivateMedia(name string) error {
	if u.ID == "" || !mediaNameRe.MatchString(name) || u.DB == nil {
		return Bad("Не удалось определить владельца файла.")
	}
	_, err := u.DB.Pool.Exec(u.Ctx, `INSERT INTO private_media(name,user_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, name, u.ID)
	if err != nil {
		return Bad("Не удалось сохранить права доступа к файлу.")
	}
	return nil
}

// Account-native downloads must never use the public URL as a cache identity.
// Register ownership before writing bytes and use the same playback conversion
// as normal downloads.
func (u *U) cachePrivateTelegramVideo(page, downloaded string) string {
	if u.ID == "" || VideoPageVia(page) != "tgu" || !fileExists(downloaded) {
		return ""
	}
	playable := u.BrowserPlayableVideo(downloaded)
	if playable != downloaded {
		defer os.Remove(playable)
	}
	ext := "mp4"
	if sniffMime(playable) == "video/webm" {
		ext = "webm"
	}
	name := MediaCacheName("account-video:"+u.ID+"\x00"+page, ext)
	if err := u.registerPrivateMedia(name); err != nil {
		return ""
	}
	return u.mediaCopyVideo(name, playable)
}
