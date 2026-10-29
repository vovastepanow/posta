package posta

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

// Тарифы, баланс в рублях и приглашения. Суммы в базе — в копейках, наружу — в рублях.
// Баланс — сумма записей ledger: пополнение (+), покупка (−), возврат остатка прежнего тарифа (+), награда за приглашённого (+).

var LedgerKinds = map[string]string{"topup": "Пополнение", "purchase": "Оплата тарифа", "refund": "Возврат остатка тарифа", "referral": "За приглашённого", "admin": "Поправка"}

func RubToKop(rub float64) int64 { return int64(math.Round(rub * 100)) }
func KopToRub(kop int64) float64  { return math.Round(float64(kop)) / 100 }

func (a *App) BalanceKop(ctx context.Context, q db.Querier, uid string) int64 {
	var n int64
	db.Value(ctx, q, &n, `SELECT COALESCE(SUM(amount), 0)::bigint FROM ledger WHERE user_id = $1`, uid)
	return n
}

func (a *App) ledgerAdd(ctx context.Context, q db.Querier, uid string, kop int64, kind, note, related string) error {
	if LedgerKinds[kind] == "" {
		return Bad("Неизвестный вид операции.")
	}
	if err := ensureUser(ctx, q, uid); err != nil {
		return err
	}
	_, err := db.Exec(ctx, q, `INSERT INTO ledger (user_id, at, amount, kind, note, related_user) VALUES ($1, $2, $3, $4, $5, $6)`, uid, now(), kop, kind, note, related)
	return err
}

// BillingTopup — пополнение (пока платежей нет — из командной строки и админки).
func (a *App) BillingTopup(ctx context.Context, uid string, rub float64, note, kind string) (int64, error) {
	if kind == "" {
		kind = "topup"
	}
	if kind == "topup" && rub <= 0 {
		return 0, Bad("Сумма пополнения должна быть больше нуля.")
	}
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error { return a.ledgerAdd(ctx, tx, uid, RubToKop(rub), kind, note, "") })
	return a.BalanceKop(ctx, a.DB.Pool, uid), err
}

func BillingHourLimit(perDay int) int { return max(1, (perDay+config.BillingHourShare-1)/config.BillingHourShare) }

func PlanDays(p config.Plan) int {
	if p.Days > 0 {
		return p.Days
	}
	return config.BillingPeriodDays
}

func (a *App) billingOnceAvailable(ctx context.Context, q db.Querier, uid string) bool {
	ok, _ := db.Exists(ctx, q, `SELECT 1 FROM ledger WHERE user_id = $1 AND kind = 'purchase'`, uid)
	return !ok
}

func BillingCustomPrice(channels, perDay int) int {
	c := config.BillingCustom
	extra := max(0, perDay-c.PostsStep)
	return c.Base + c.Channel*max(0, channels-1) + c.PostsStepPrice*((extra+c.PostsStep-1)/c.PostsStep)
}

// BillingPlanSpec — готовый тариф или «свой».
func BillingPlanSpec(plan string, channels, perDay int) (config.Plan, error) {
	if p, ok := config.PlanByKey(plan); ok {
		return p, nil
	}
	if plan != "custom" {
		return config.Plan{}, Bad("Неизвестный тариф.")
	}
	c := config.BillingCustom
	if channels < 1 || channels > c.MaxChannels {
		return config.Plan{}, Bad("Каналов можно от 1 до %d.", c.MaxChannels)
	}
	if perDay < c.PostsStep || perDay > c.MaxPostsPerDay || perDay%c.PostsStep != 0 {
		return config.Plan{}, Bad("Постов в день — от %d до %d, шагом %d.", c.PostsStep, c.MaxPostsPerDay, c.PostsStep)
	}
	return config.Plan{Key: "custom", Title: "Свой тариф", Tag: "EGEN", Price: BillingCustomPrice(channels, perDay), Channels: channels, PostsPerDay: perDay, Schedule: true}, nil
}

// BillingCheaperPlan — готовый тариф, который даёт не меньше и стоит дешевле.
func BillingCheaperPlan(spec config.Plan) *config.Plan {
	for _, p := range config.BillingPlans {
		if p.Key != spec.Key && !p.Once && p.Price > 0 && p.Price < spec.Price && p.Channels >= spec.Channels && p.PostsPerDay >= spec.PostsPerDay {
			return &p
		}
	}
	return nil
}

type Subscription struct {
	Plan                 string
	Channels, PostsPerDay int
	Price, StartedAt, Until int64
}

func (a *App) billingSubscription(ctx context.Context, q db.Querier, uid string) *Subscription {
	var s Subscription
	err := q.QueryRow(ctx, `SELECT plan, channels, posts_per_day, price, started_at, until FROM subscriptions WHERE user_id = $1 AND until > $2`, uid, now()).
		Scan(&s.Plan, &s.Channels, &s.PostsPerDay, &s.Price, &s.StartedAt, &s.Until)
	if err != nil {
		return nil
	}
	return &s
}

func planTitle(key string) string {
	if p, ok := config.PlanByKey(key); ok {
		return p.Title
	}
	return "Свой тариф"
}

// BillingLimits — лимиты пользователя: подписка или бесплатный тариф.
func (a *App) BillingLimitsOf(ctx context.Context, uid string) jx.M {
	var spec jx.M
	if s := a.billingSubscription(ctx, a.DB.Pool, uid); s != nil {
		sched := true
		if p, ok := config.PlanByKey(s.Plan); ok {
			sched = p.Schedule
		}
		spec = jx.M{"id": s.Plan, "title": planTitle(s.Plan), "channels": s.Channels, "posts_per_day": s.PostsPerDay, "schedule": sched, "until": s.Until, "price": KopToRub(s.Price)}
	} else {
		p, _ := config.PlanByKey(config.BillingFreePlan)
		spec = jx.M{"id": p.Key, "title": p.Title, "tag": p.Tag, "price": p.Price, "channels": p.Channels, "posts_per_day": p.PostsPerDay, "schedule": p.Schedule, "until": 0}
	}
	spec["posts_per_hour"] = BillingHourLimit(int(jx.Int(spec, "posts_per_day")))
	return spec
}

func (u *U) BillingLimits() jx.M { return u.BillingLimitsOf(u.Ctx, u.ID) }

// BillingBuy — покупка с баланса. Тот же тариф — продление; другой — остаток прежнего возвращается и новый начинается сегодня.
func (u *U) BillingBuy(plan string, channels, perDay int) (jx.M, error) {
	spec, err := BillingPlanSpec(plan, channels, perDay)
	if err != nil {
		return nil, err
	}
	if spec.Price <= 0 {
		return nil, Bad("Бесплатный тариф покупать не нужно: он включается сам, когда платный заканчивается.")
	}
	ctx := u.Ctx
	err = u.DB.Tx(ctx, func(tx pgx.Tx) error {
		tx.Exec(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, u.ID)
		if spec.Once && !u.billingOnceAvailable(ctx, tx, u.ID) {
			return Conflict("«%s» можно взять только один раз и до первой покупки тарифа.", spec.Title)
		}
		t := now()
		days := PlanDays(spec)
		period := int64(days) * 86400
		price := RubToKop(float64(spec.Price))
		sub := u.billingSubscription(ctx, tx, u.ID)
		same := sub != nil && sub.Plan == spec.Key && sub.Channels == spec.Channels && sub.PostsPerDay == spec.PostsPerDay
		var refund int64
		if sub != nil && !same {
			refund = int64(math.Floor(float64(sub.Price) * float64(max(0, sub.Until-t)) / float64(max(1, sub.Until-sub.StartedAt))))
		}
		balance := u.BalanceKop(ctx, tx, u.ID)
		if balance+refund < price {
			return Conflict("Не хватает %g ₽ — пополните баланс.", KopToRub(price-balance-refund))
		}
		if refund > 0 {
			if err := u.ledgerAdd(ctx, tx, u.ID, refund, "refund", "Остаток тарифа «"+planTitle(sub.Plan)+"»", ""); err != nil {
				return err
			}
		}
		if err := u.ledgerAdd(ctx, tx, u.ID, -price, "purchase", fmt.Sprintf("%s: %d кан., %d пост./день, %d дн.", spec.Title, spec.Channels, spec.PostsPerDay, days), ""); err != nil {
			return err
		}
		start, until, total := t, t+period, price
		if same {
			start, until, total = sub.StartedAt, sub.Until+period, sub.Price+price
		}
		if _, err := tx.Exec(ctx, `INSERT INTO subscriptions (user_id, plan, channels, posts_per_day, price, started_at, until) VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (user_id) DO UPDATE SET plan = EXCLUDED.plan, channels = EXCLUDED.channels, posts_per_day = EXCLUDED.posts_per_day, price = EXCLUDED.price,
			started_at = EXCLUDED.started_at, until = EXCLUDED.until`, u.ID, spec.Key, spec.Channels, spec.PostsPerDay, total, start, until); err != nil {
			return err
		}
		// Награда пригласившему — только с новых денег: возврат остатка уже был оплачен раньше
		var referrer string
		db.Value(ctx, tx, &referrer, `SELECT COALESCE(referred_by, '') FROM users WHERE id = $1`, u.ID)
		reward := max(0, price-refund) * config.ReferralPercent / 100
		if referrer != "" && reward > 0 && u.UserExists(ctx, referrer) {
			return u.ledgerAdd(ctx, tx, referrer, reward, "referral", "Приглашённый оплатил «"+spec.Title+"»", u.ID)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u.BillingLimits(), nil
}

func (u *U) BillingPostsSince(since int64) int {
	var n int
	db.Value(u.Ctx, u.DB.Pool, &n, `SELECT COUNT(*) FROM post_usage WHERE user_id = $1 AND at >= $2`, u.ID, since)
	return n
}

// BillingPostDenied — причина отказа, если пост сверх лимита тарифа; "" — можно. Лимиты включаются в админке.
func (u *U) BillingPostDenied(scheduled bool) string {
	if !u.TBool("billing_enforce") {
		return ""
	}
	l := u.BillingLimits()
	title := jx.Str(l, "title")
	switch {
	case scheduled && !jx.Bool(l, "schedule"):
		return fmt.Sprintf("На тарифе «%s» посты публикуются только сразу. Отложенные посты — с тарифа «Автопилот».", title)
	case u.BillingPostsSince(now()-3600) >= int(jx.Int(l, "posts_per_hour")):
		return fmt.Sprintf("Лимит тарифа «%s»: не больше %d пост. в час. Следующий — чуть позже.", title, jx.Int(l, "posts_per_hour"))
	case u.BillingPostsSince(now()-86400) >= int(jx.Int(l, "posts_per_day")):
		return fmt.Sprintf("Лимит тарифа «%s»: %d пост. в сутки исчерпан. Больше постов — в «Личном кабинете» → «Тариф и баланс».", title, jx.Int(l, "posts_per_day"))
	}
	return ""
}

// BillingPostUsed — учёт отправленного поста; старше двух суток записи не нужны.
func (u *U) BillingPostUsed() {
	u.DB.Pool.Exec(u.Ctx, `INSERT INTO post_usage (user_id, at) VALUES ($1, $2)`, u.ID, now())
	u.DB.Pool.Exec(u.Ctx, `DELETE FROM post_usage WHERE user_id = $1 AND at < $2`, u.ID, now()-2*86400)
}

// ---- Приглашения ----

func (u *U) ReferralCode() (string, error) {
	var code string
	db.Value(u.Ctx, u.DB.Pool, &code, `SELECT COALESCE(ref_code, '') FROM users WHERE id = $1`, u.ID)
	if code != "" {
		return code, nil
	}
	for try := 0; try < 5; try++ {
		b := make([]byte, 6)
		rand.Read(b)
		code = strings.NewReplacer("+", "x", "/", "z").Replace(base64.StdEncoding.EncodeToString(b))[:8]
		if _, err := u.DB.Pool.Exec(u.Ctx, `UPDATE users SET ref_code = $1 WHERE id = $2 AND ref_code IS NULL`, code, u.ID); err != nil {
			continue
		}
		db.Value(u.Ctx, u.DB.Pool, &code, `SELECT COALESCE(ref_code, '') FROM users WHERE id = $1`, u.ID)
		return code, nil
	}
	return "", fmt.Errorf("не удалось создать код приглашения")
}

// ReferralBind — привязать нового пользователя к пригласившему: причина отказа или "".
func (u *U) ReferralBind(code string) string {
	var reason string
	u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		var referrer, referredBy string
		var created int64
		db.Value(u.Ctx, tx, &referrer, `SELECT id FROM users WHERE ref_code = $1`, strings.TrimSpace(code))
		found, _ := db.Value(u.Ctx, tx, &created, `SELECT created_at FROM users WHERE id = $1`, u.ID)
		db.Value(u.Ctx, tx, &referredBy, `SELECT COALESCE(referred_by, '') FROM users WHERE id = $1`, u.ID)
		switch {
		case referrer == "":
			reason = "Такой ссылки-приглашения нет."
		case referrer == u.ID:
			reason = "Это ваша собственная ссылка."
		case !found || referredBy != "":
			reason = "Приглашение уже засчитано раньше."
		case now()-created > config.ReferralBindDays*86400 || !u.billingOnceAvailable(u.Ctx, tx, u.ID):
			reason = "Приглашение действует только для новых пользователей."
		default:
			if _, err := tx.Exec(u.Ctx, `UPDATE users SET referred_by = $1 WHERE id = $2`, referrer, u.ID); err != nil {
				reason = "Не удалось засчитать приглашение."
			}
		}
		return nil
	})
	return reason
}

func (u *U) ReferralStats() jx.M {
	var invited, paid int
	var earned int64
	db.Value(u.Ctx, u.DB.Pool, &invited, `SELECT COUNT(*) FROM users WHERE referred_by = $1`, u.ID)
	db.Value(u.Ctx, u.DB.Pool, &paid, `SELECT COUNT(DISTINCT related_user) FROM ledger WHERE user_id = $1 AND kind = 'referral'`, u.ID)
	db.Value(u.Ctx, u.DB.Pool, &earned, `SELECT COALESCE(SUM(amount), 0)::bigint FROM ledger WHERE user_id = $1 AND kind = 'referral'`, u.ID)
	return jx.M{"invited": invited, "paid": paid, "earned": KopToRub(earned)}
}

// BillingPublic — тариф, баланс, история и приглашения для «Личного кабинета».
func (u *U) BillingPublic() jx.M {
	rows, _ := db.All(u.Ctx, u.DB.Pool, `SELECT at, amount, kind, note FROM ledger WHERE user_id = $1 ORDER BY id DESC LIMIT 50`, u.ID)
	ledger := []jx.M{}
	for _, r := range rows {
		ledger = append(ledger, jx.M{"at": r["at"], "amount": KopToRub(jx.ToInt(r["amount"])), "kind": r["kind"], "kind_title": LedgerKinds[jx.ToStr(r["kind"])], "note": r["note"]})
	}
	code, _ := u.ReferralCode()
	plans := []jx.M{}
	for _, p := range config.BillingPlans {
		plans = append(plans, jx.M{"id": p.Key, "title": p.Title, "tag": p.Tag, "price": p.Price, "channels": p.Channels, "posts_per_day": p.PostsPerDay,
			"schedule": p.Schedule, "days": PlanDays(p), "once": p.Once, "posts_per_hour": BillingHourLimit(p.PostsPerDay)})
	}
	return jx.M{"limits": u.BillingLimits(), "balance": KopToRub(u.BalanceKop(u.Ctx, u.DB.Pool, u.ID)), "ledger": ledger, "plans": plans,
		"custom": config.BillingCustom, "once_available": u.billingOnceAvailable(u.Ctx, u.DB.Pool, u.ID), "enforce": u.TBool("billing_enforce"),
		"referral": jx.Merge(u.ReferralStats(), jx.M{"code": code, "percent": config.ReferralPercent}), "posts_today": u.BillingPostsSince(now() - 86400)}
}

var _ = time.Second
