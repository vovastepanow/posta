<?php
// Тарифы, баланс в рублях и приглашения. Суммы в базе — в копейках (целые), наружу — в рублях.
// Баланс — сумма записей ledger: пополнение (+), покупка (−), возврат остатка прежнего тарифа (+), награда за приглашённого (+).

const LEDGER_KINDS = ['topup' => 'Пополнение', 'purchase' => 'Оплата тарифа', 'refund' => 'Возврат остатка тарифа', 'referral' => 'За приглашённого', 'admin' => 'Поправка'];

function rub_to_kop($rub) { return (int)round((float)$rub * 100); }
function kop_to_rub($kop) { return round($kop / 100, 2); }

function balance_kop($uid = null) {
    return (int)db_value('SELECT COALESCE(SUM(amount), 0) FROM ledger WHERE user_id = ?', [$uid ?: app_user_id()]);
}

function ledger_add($uid, $kop, $kind, $note = '', $related = '') {
    if (!isset(LEDGER_KINDS[$kind])) throw new InvalidArgumentException('Неизвестный вид операции.');
    db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
    db_exec('INSERT INTO ledger (user_id, at, amount, kind, note, related_user) VALUES (?, ?, ?, ?, ?, ?)', [$uid, time(), (int)$kop, $kind, (string)$note, (string)$related]);
}

/** Пополнение баланса. Пока приёма платежей нет — из tools/cli.php (balance-add); потом сюда же придёт подтверждение платёжки. */
function billing_topup($uid, $rub, $note = '') {
    if ($rub <= 0) throw new InvalidArgumentException('Сумма пополнения должна быть больше нуля.');
    db_tx(fn() => ledger_add($uid, rub_to_kop($rub), 'topup', $note));
    return balance_kop($uid);
}

// ---- Тарифы ----

function billing_hour_limit($per_day) { return max(1, (int)ceil($per_day / BILLING_HOUR_SHARE)); }

/** Срок тарифа в днях: у пробного свой, у остальных — общий период. */
function billing_plan_days(array $spec) { return (int)($spec['days'] ?? BILLING_PERIOD_DAYS); }

/** Разовый тариф («Хот-дог») доступен, пока пользователь ничего не покупал. */
function billing_once_available($uid) {
    return !db_value("SELECT 1 FROM ledger WHERE user_id = ? AND kind = 'purchase' LIMIT 1", [$uid]);
}

/** Цена «своего тарифа» в рублях. */
function billing_custom_price($channels, $per_day) {
    $c = BILLING_CUSTOM;
    $extra_posts = max(0, $per_day - $c['posts_step']);
    return $c['base'] + $c['channel'] * max(0, $channels - 1) + $c['posts_step_price'] * (int)ceil($extra_posts / $c['posts_step']);
}

/** Тариф по запросу: готовый (plan) или свой (channels, posts_per_day). [id, title, price ₽, channels, posts_per_day, schedule]. */
function billing_plan_spec($plan, $channels = 0, $per_day = 0) {
    if (isset(BILLING_PLANS[$plan])) return ['id' => $plan] + BILLING_PLANS[$plan];
    if ($plan !== 'custom') throw new InvalidArgumentException('Неизвестный тариф.');
    $c = BILLING_CUSTOM;
    $channels = (int)$channels;
    $per_day = (int)$per_day;
    if ($channels < 1 || $channels > $c['max_channels']) throw new InvalidArgumentException("Каналов можно от 1 до {$c['max_channels']}.");
    if ($per_day < $c['posts_step'] || $per_day > $c['max_posts_per_day'] || $per_day % $c['posts_step']) {
        throw new InvalidArgumentException("Постов в день — от {$c['posts_step']} до {$c['max_posts_per_day']}, шагом {$c['posts_step']}.");
    }
    return ['id' => 'custom', 'title' => 'Свой тариф', 'tag' => 'EGEN', 'price' => billing_custom_price($channels, $per_day), 'channels' => $channels, 'posts_per_day' => $per_day, 'schedule' => true];
}

/** Готовый тариф, который даёт не меньше и стоит дешевле, — подсказка в калькуляторе. */
function billing_cheaper_plan(array $spec) {
    foreach (BILLING_PLANS as $id => $p) {
        if ($id !== $spec['id'] && empty($p['once']) && $p['price'] > 0 && $p['price'] < $spec['price'] && $p['channels'] >= $spec['channels'] && $p['posts_per_day'] >= $spec['posts_per_day']) return ['id' => $id] + $p;
    }
    return null;
}

/** Действующая подписка или null (истекла — значит бесплатный тариф). */
function billing_subscription($uid = null) {
    $row = db_one('SELECT * FROM subscriptions WHERE user_id = ? AND until > ?', [$uid ?: app_user_id(), time()]);
    return $row ? array_map(fn($v) => is_numeric($v) ? (int)$v : $v, $row) : null;
}

/** Текущие лимиты пользователя: подписка или бесплатный тариф. */
function billing_limits($uid = null) {
    $sub = billing_subscription($uid);
    $spec = $sub ? ['id' => $sub['plan'], 'title' => BILLING_PLANS[$sub['plan']]['title'] ?? 'Свой тариф', 'channels' => $sub['channels'], 'posts_per_day' => $sub['posts_per_day'],
        'schedule' => BILLING_PLANS[$sub['plan']]['schedule'] ?? true, 'until' => $sub['until'], 'price' => kop_to_rub($sub['price'])]
        : ['id' => BILLING_FREE_PLAN, 'until' => 0] + BILLING_PLANS[BILLING_FREE_PLAN];
    return $spec + ['posts_per_hour' => billing_hour_limit($spec['posts_per_day'])];
}

/** Покупка тарифа с баланса. Тот же тариф — продление; другой — остаток прежнего возвращается на баланс и новый начинается сегодня. */
function billing_buy($uid, $plan, $channels = 0, $per_day = 0) {
    $spec = billing_plan_spec($plan, $channels, $per_day);
    if ($spec['price'] <= 0) throw new InvalidArgumentException('Бесплатный тариф покупать не нужно: он включается сам, когда платный заканчивается.');
    return db_tx(function () use ($uid, $spec) {
        if (!empty($spec['once']) && !billing_once_available($uid)) throw new DomainException("«{$spec['title']}» можно взять только один раз и до первой покупки тарифа.");
        $now = time();
        $days = billing_plan_days($spec);
        $period = $days * DAY_SECONDS;
        $price = rub_to_kop($spec['price']);
        $sub = billing_subscription($uid);
        $same = $sub && $sub['plan'] === $spec['id'] && $sub['channels'] === $spec['channels'] && $sub['posts_per_day'] === $spec['posts_per_day'];
        $refund = $sub && !$same ? (int)floor($sub['price'] * max(0, $sub['until'] - $now) / max(1, $sub['until'] - $sub['started_at'])) : 0;
        $balance = balance_kop($uid);
        if ($balance + $refund < $price) throw new DomainException('Не хватает ' . kop_to_rub($price - $balance - $refund) . ' ₽ — пополните баланс.');

        if ($refund > 0) ledger_add($uid, $refund, 'refund', 'Остаток тарифа «' . (BILLING_PLANS[$sub['plan']]['title'] ?? 'Свой тариф') . '»');
        ledger_add($uid, -$price, 'purchase', "{$spec['title']}: {$spec['channels']} кан., {$spec['posts_per_day']} пост./день, {$days} дн.");
        [$start, $until] = $same ? [$sub['started_at'], $sub['until'] + $period] : [$now, $now + $period];
        db_exec('INSERT OR REPLACE INTO subscriptions (user_id, plan, channels, posts_per_day, price, started_at, until) VALUES (?, ?, ?, ?, ?, ?, ?)',
            [$uid, $spec['id'], $spec['channels'], $spec['posts_per_day'], $same ? $sub['price'] + $price : $price, $start, $until]);

        // Награда пригласившему — только с новых денег: возврат остатка уже был оплачен раньше
        $referrer = (string)db_value('SELECT referred_by FROM users WHERE id = ?', [$uid]);
        $reward = (int)floor(max(0, $price - $refund) * REFERRAL_PERCENT / 100);
        if ($referrer !== '' && $reward > 0 && db_value('SELECT 1 FROM users WHERE id = ?', [$referrer])) {
            ledger_add($referrer, $reward, 'referral', "Приглашённый оплатил «{$spec['title']}»", $uid);
        }
        return billing_limits($uid);
    });
}

// ---- Лимиты постов ----

function billing_posts_since($since, $uid = null) {
    return (int)db_value('SELECT COUNT(*) FROM post_usage WHERE user_id = ? AND at >= ?', [$uid ?: app_user_id(), $since]);
}

/** Текст отказа, если пост сверх лимита тарифа; null — можно. При BILLING_ENFORCE = false лимиты не проверяются. */
function billing_post_denied($scheduled, $uid = null) {
    if (!BILLING_ENFORCE) return null;
    $l = billing_limits($uid);
    if ($scheduled && !$l['schedule']) return "На тарифе «{$l['title']}» посты публикуются только сразу. Отложенные посты — с тарифа «Автопилот».";
    if (billing_posts_since(time() - 3600, $uid) >= $l['posts_per_hour']) return "Лимит тарифа «{$l['title']}»: не больше {$l['posts_per_hour']} пост. в час. Следующий — чуть позже.";
    if (billing_posts_since(time() - DAY_SECONDS, $uid) >= $l['posts_per_day']) return "Лимит тарифа «{$l['title']}»: {$l['posts_per_day']} пост. в сутки исчерпан. Больше постов — в «Личном кабинете» → «Тариф и баланс».";
    return null;
}

/** Учёт отправленного или поставленного в отложенные поста. Старше двух суток записи не нужны. */
function billing_post_used($uid = null) {
    $uid = $uid ?: app_user_id();
    db_exec('INSERT INTO post_usage (user_id, at) VALUES (?, ?)', [$uid, time()]);
    db_exec('DELETE FROM post_usage WHERE user_id = ? AND at < ?', [$uid, time() - 2 * DAY_SECONDS]);
}

// ---- Приглашения ----

/** Код приглашения пользователя; создаётся при первом запросе. */
function referral_code($uid = null) {
    $uid = $uid ?: app_user_id();
    $code = (string)db_value('SELECT ref_code FROM users WHERE id = ?', [$uid]);
    if ($code !== '') return $code;
    for ($try = 0; $try < 5; $try++) {
        $code = substr(strtr(base64_encode(random_bytes(6)), '+/', 'xz'), 0, 8);
        try { db_exec('UPDATE users SET ref_code = ? WHERE id = ? AND ref_code IS NULL', [$code, $uid]); }
        catch (PDOException $e) { continue; }   // такой код уже занят
        return (string)db_value('SELECT ref_code FROM users WHERE id = ?', [$uid]);
    }
    throw new RuntimeException('Не удалось создать код приглашения.');
}

/** Привязать нового пользователя к пригласившему. Возвращает причину отказа или '' — привязан. */
function referral_bind($uid, $code) {
    return db_tx(function () use ($uid, $code) {
        $referrer = (string)db_value('SELECT id FROM users WHERE ref_code = ?', [trim((string)$code)]);
        $me = db_one('SELECT created_at, referred_by FROM users WHERE id = ?', [$uid]);
        return match (true) {
            $referrer === '' => 'Такой ссылки-приглашения нет.',
            $referrer === $uid => 'Это ваша собственная ссылка.',
            !$me || $me['referred_by'] !== '' => 'Приглашение уже засчитано раньше.',
            time() - (int)$me['created_at'] > REFERRAL_BIND_DAYS * DAY_SECONDS || db_value("SELECT 1 FROM ledger WHERE user_id = ? AND kind = 'purchase' LIMIT 1", [$uid])
                => 'Приглашение действует только для новых пользователей.',
            default => db_exec('UPDATE users SET referred_by = ? WHERE id = ?', [$referrer, $uid]) ? '' : 'Не удалось засчитать приглашение.',
        };
    });
}

function referral_stats($uid = null) {
    $uid = $uid ?: app_user_id();
    return ['invited' => (int)db_value('SELECT COUNT(*) FROM users WHERE referred_by = ?', [$uid]),
        'paid' => (int)db_value("SELECT COUNT(DISTINCT related_user) FROM ledger WHERE user_id = ? AND kind = 'referral'", [$uid]),
        'earned' => kop_to_rub((int)db_value("SELECT COALESCE(SUM(amount), 0) FROM ledger WHERE user_id = ? AND kind = 'referral'", [$uid]))];
}
