<?php
// Общий OAuth-поток соцсетей (AUTOPOST_NETWORKS_PROMPT.md, часть C). Ключи приложения — в config.local.php (имена констант
// в реестре, поле app_keys); без них сеть не подключается и показывается «нужен ключ приложения на сервере».
// Состояние входа (state, PKCE-код) — в kv на 15 минут; токены — в secrets_enc ресурса; обновляются перед отправкой.

const NETWORK_STATE_SECONDS = 900;
const NETWORK_REFRESH_MARGIN = 120;                 // обновить токен, если до конца срока меньше двух минут

/** Ключ приложения из констант конфига; пусто — не задан. */
function network_app_key(string $name): string {
    return defined($name) ? trim((string)constant($name)) : '';
}

/** Все ключи соцсети заданы — сеть можно подключать (бета). */
function network_keys_present(array $names): bool {
    foreach ($names as $name) if (network_app_key($name) === '') return false;
    return $names !== [];
}

/** Адрес, куда провайдер вернёт пользователя: NETWORK_REDIRECT_BASE из конфига (публичный адрес сайта) + обработчик. */
function network_redirect_uri(): string {
    return rtrim(defined('NETWORK_REDIRECT_BASE') ? (string)NETWORK_REDIRECT_BASE : 'http://localhost', '/') . '/index.php?action=network_oauth_callback';
}

/** Новое состояние входа: сохраняет пользователя, соцсеть и PKCE-код. Возвращает state. */
function network_oauth_state_create(string $uid, string $platform, string $verifier = ''): string {
    $state = bin2hex(random_bytes(16));
    kv_update('netoauth:' . $state, fn() => ['uid' => $uid, 'platform' => $platform, 'verifier' => $verifier, 'at' => time()]);
    return $state;
}

/** Состояние входа: забирает и удаляет (одноразовое); просроченное или неизвестное — null. */
function network_oauth_state_take(string $state): ?array {
    if (!preg_match('/^[a-f0-9]{32}$/', $state)) return null;
    $data = kv_get('netoauth:' . $state, null);
    db_exec('DELETE FROM kv WHERE key = ?', ['netoauth:' . $state]);
    if (!is_array($data) || time() - (int)($data['at'] ?? 0) > NETWORK_STATE_SECONDS) return null;
    return $data;
}

/**
 * Обновляет токен ресурса, если срок близко (и есть refresh-функция сети). Новые секреты сохраняются в ресурс.
 * Возвращает канал с актуальными секретами; ошибка обновления — исключение с понятным текстом.
 */
function network_refresh_channel(array $channel): array {
    $network = networks_registry()[$channel['platform']] ?? [];
    $secrets = (array)($channel['secrets'] ?? []);
    if (empty($network['oauth']['refresh']) || empty($secrets['refresh_token']) || (int)($secrets['expires_at'] ?? 0) > time() + NETWORK_REFRESH_MARGIN) return $channel;
    $fresh = ($network['oauth']['refresh'])($secrets);
    $channel['secrets'] = $fresh;
    if (!empty($channel['user_id'])) {
        channel_save(['id' => $channel['id'], 'platform' => $channel['platform'], 'ref' => $channel['ref'], 'secrets' => $fresh, 'meta' => $channel['meta'] ?? [],
            'chat_title' => $channel['chat_title'] ?? '', 'chat_id' => 0, 'connected_at' => $channel['connected_at'] ?? time()], $channel['user_id']);
    }
    return $channel;
}

/** Срок токена из ответа провайдера: unix-время конца. */
function network_expires_at(array $token): int {
    return time() + (int)($token['expires_in'] ?? 3600);
}
