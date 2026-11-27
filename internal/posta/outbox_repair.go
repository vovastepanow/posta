package posta

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Install only files staged for the same unchanged queued/failed post.
// Never rewrite an active publisher claim or a user's newer edit.
func (u *U) applyOutboxRepair(original, updated jx.M, staged map[string]string) (bool, error) {
	id := jx.Str(original, "id")
	dir, err := u.OutboxMediaDir(id)
	if err != nil {
		return false, err
	}
	applied := false
	var installErr error
	_, err = u.OutboxUpdate(func(q []jx.M) []jx.M {
		for i, current := range q {
			if jx.Str(current, "id") != id {
				continue
			}
			if !contains([]string{"queued", "failed"}, jx.Str(current, "status")) ||
				jx.Has(current, "confirmed_receipt") ||
				string(jx.Marshal(current)) != string(jx.Marshal(original)) {
				return nil
			}
			if len(staged) > 0 {
				if installErr = os.MkdirAll(dir, 0775); installErr != nil {
					return nil
				}
			}
			for dest, src := range staged {
				if filepath.Clean(dest) != filepath.Join(dir, filepath.Base(dest)) {
					installErr = Bad("Некорректный путь файла очереди.")
					return nil
				}
				if !fileExists(dest) {
					if installErr = os.Rename(src, dest); installErr != nil {
						return nil
					}
				}
			}
			// A requeue requires the actual complete payload, not just
			// a successful download attempt.
			if jx.Str(original, "status") == "failed" && jx.Str(updated, "status") == "queued" && !outboxMediaReady(updated) {
				installErr = Bad("Не все файлы публикации восстановлены.")
				return nil
			}
			q[i] = updated
			applied = true
			return q
		}
		return nil
	})
	if err != nil {
		return false, err
	}
	return applied, installErr
}

// OutboxRepair restores missing files without allowing partially copied
// bytes to become visible to a publisher or overwriting newer queue state.
func (a *App) OutboxRepair(ctx context.Context, apply bool, onlyUID string) []string {
	var report []string
	var users []string
	if onlyUID != "" {
		users = []string{onlyUID}
	} else {
		rows, err := a.DB.Pool.Query(ctx, `SELECT DISTINCT user_id FROM outbox`)
		if err != nil {
			return []string{"Не удалось прочитать очередь: " + err.Error()}
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return []string{"Не удалось прочитать очередь: " + err.Error()}
			}
			users = append(users, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return []string{"Не удалось прочитать очередь: " + err.Error()}
		}
	}
	for _, uid := range users {
		u := a.User(ctx, uid, nil)
		for _, post := range u.Outbox() {
			if ctx.Err() != nil {
				return append(report, "Проверка прервана.")
			}
			if !contains([]string{"queued", "failed"}, jx.Str(post, "status")) || jx.Has(post, "confirmed_receipt") {
				continue
			}
			report = append(report, u.repairOneOutbox(post, apply)...)
		}
	}
	if len(report) == 0 {
		return []string{"Проблем с очередью не найдено."}
	}
	return report
}

func (u *U) repairOneOutbox(post jx.M, apply bool) []string {
	id, st := jx.Str(post, "id"), jx.Str(post, "status")
	dir, err := u.OutboxMediaDir(id)
	if err != nil {
		return []string{id + ": некорректный идентификатор очереди"}
	}
	var lines []string
	ok := true
	staged := map[string]string{}
	stageDir := ""
	defer func() {
		if stageDir != "" {
			os.RemoveAll(stageDir)
		}
	}()
	for k, f := range jx.Maps(post, "media") {
		if cover := jx.Str(f, "cover_path"); cover != "" && !fileExists(cover) {
			ok = false
			lines = append(lines, fmt.Sprintf("файл %d: пользовательская обложка отсутствует, добавьте её заново", k+1))
		}
		dest := jx.Str(f, "path")
		if fileExists(dest) {
			continue
		}
		if dest == "" || filepath.Clean(dest) != filepath.Join(dir, filepath.Base(dest)) {
			ok = false
			lines = append(lines, fmt.Sprintf("файл %d: некорректный путь; требуется редактор", k+1))
			continue
		}
		src := jx.Str(f, "src")
		cache := u.MediaPath(src)
		if cache != "" && !u.CachedMediaAllowed(u.Ctx, u.ID, filepath.Base(cache)) {
			ok = false
			lines = append(lines, fmt.Sprintf("файл %d: доступ к кэшу запрещён", k+1))
			continue
		}
		local := cache != "" && fileExists(cache)
		if local {
			lines = append(lines, fmt.Sprintf("файл %d: найден в кэше", k+1))
		} else if strings.HasPrefix(src, "https://") || strings.HasPrefix(src, "http://") {
			lines = append(lines, fmt.Sprintf("файл %d: требуется загрузка по ссылке", k+1))
		} else {
			ok = false
			lines = append(lines, fmt.Sprintf("файл %d: не найден ни в кэше, ни по ссылке", k+1))
			continue
		}
		if !apply {
			continue
		}
		if stageDir == "" {
			if err = os.MkdirAll(filepath.Dir(dir), 0775); err == nil {
				stageDir, err = os.MkdirTemp(filepath.Dir(dir), ".repair-"+id+"-")
			}
			if err != nil {
				ok = false
				lines = append(lines, "Не удалось создать временную папку ремонта.")
				break
			}
		}
		temp := filepath.Join(stageDir, fmt.Sprintf("%d-%s", k, filepath.Base(dest)))
		if local {
			err = copyFile(cache, temp)
		} else {
			err = u.fetchPublicMedia(src, temp, config.TelegramUploadMaxBytes)
		}
		if err != nil {
			ok = false
			lines = append(lines, fmt.Sprintf("файл %d: %s", k+1, err))
			continue
		}
		staged[dest] = temp
	}
	nw := jx.Copy(post)
	if clean := PostCaptionClean(jx.Str(post, "text")); clean != jx.Str(post, "text") {
		nw["text"] = clean
		lines = append(lines, "заглушка в подписи очищена")
	}
	if st == "failed" && ok && strings.Contains(strings.ToLower(jx.Str(post, "error")), "файлы поста не найдены") {
		if jx.Int(post, "publish_at") > now() {
			for k, v := range (jx.M{"status": "queued", "error": "", "attempts": 0, "next_try_at": 0}) {
				nw[k] = v
			}
			lines = append(lines, "восстановление очереди возможно")
		} else {
			lines = append(lines, "время уже прошло — остаётся «не вышел»")
		}
	} else if !ok {
		lines = append(lines, "не все файлы можно восстановить")
	}
	if len(lines) == 0 {
		return nil
	}
	tag := " · (проверка)"
	if apply {
		tag = ""
		applied, err := u.applyOutboxRepair(post, nw, staged)
		switch {
		case err != nil:
			lines = append(lines, "Ремонт не завершён: "+err.Error())
		case !applied:
			lines = append(lines, "Пост изменился или отправляется; старый снимок не применён.")
		case jx.Str(nw, "status") == "queued" && st == "failed":
			lines = append(lines, "Пост возвращён в очередь.")
		}
	}
	report := []string{fmt.Sprintf("%s · %s · %s · %s%s", u.ID[:min(8, len(u.ID))], id, time.Unix(jx.Int(post, "publish_at"), 0).Format("15:04 02.01"), st, tag)}
	for _, line := range lines {
		report = append(report, "    "+line)
	}
	return report
}
