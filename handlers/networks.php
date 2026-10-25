<?php
// Ресурсы соцсетей кроме Telegram: список, подключение по полям реестра, отключение (AUTOPOST_NETWORKS_PROMPT.md, часть A.5).
require_once __DIR__ . '/../lib/radar_admin.php';   // владелец — для бета-сетей
// Секреты не возвращаются в ответах: в интерфейс уходят только id, платформа, название и ref.

/** Ресурсы соцсетей кроме Telegram для интерфейса. */
function networks_channels_public($uid = null): array {
    $out = [];
    foreach (channels_load($uid) as $c) {
        if ($c['platform'] !== 'telegram') $out[] = ['id' => $c['id'], 'platform' => $c['platform'], 'title' => $c['chat_title'], 'ref' => $c['ref'], 'connected_at' => $c['connected_at']];
    }
    return $out;
}

function handle_networks_list() {
    ok(['networks' => networks_public(), 'channels' => networks_channels_public()]);
}

/** Сохраняет ресурс соцсети: дубль (платформа, ref) и лимит тарифа проверяются здесь. Возвращает текст ошибки или null. */
function network_save_resource(string $uid, string $platform, array $network, array $res): ?string {
    $list = channels_load($uid);
    foreach ($list as $c) if ($c['platform'] === $platform && $c['ref'] === $res['ref']) return $network['title'] . ': этот ресурс уже подключён.';
    if (BILLING_ENFORCE && count($list) >= billing_limits($uid)['channels']) return 'На тарифе «' . billing_limits($uid)['title'] . '» можно подключить ресурсов: ' . billing_limits($uid)['channels'] . '. Больше — в «Тариф и баланс».';
    channel_save(['platform' => $platform, 'ref' => $res['ref'], 'secrets' => $res['secrets'], 'meta' => $res['meta'] ?? [], 'chat_title' => $res['title'] ?? $network['title'], 'chat_id' => 0], $uid);
    return null;
}

/** Адрес интерфейса после входа: результат — в параметрах. */
function network_back(array $params): never {
    header('Location: ' . rtrim(defined('NETWORK_REDIRECT_BASE') ? (string)NETWORK_REDIRECT_BASE : 'http://localhost', '/') . '/?' . http_build_query($params), true, 302);
    exit;
}

/** GET ?platform=: начать вход в соцсеть — перенаправление на страницу провайдера. Ключи приложения должны быть заданы. */
function handle_network_oauth_start($in) {
    $platform = (string)($_GET['platform'] ?? '');
    $network = networks_registry()[$platform] ?? null;
    if (!$network || empty($network['oauth'])) json_error('Эта соцсеть подключается не входом.', 422);
    if (!network_keys_present($network['app_keys'] ?? [])) json_error($network['title'] . ': не настроено на сервере (нужны ключи приложения).', 422);
    if ($network['status'] === 'beta' && !radar_is_owner(app_user_id())) json_error($network['title'] . ' в бете: подключает пока только владелец.', 403);
    $verifier = !empty($network['oauth']['pkce']) ? rtrim(strtr(base64_encode(random_bytes(48)), '+/', '-_'), '=') : '';
    $state = network_oauth_state_create(app_user_id(), $platform, $verifier);
    header('Location: ' . ($network['oauth']['authorize'])($state, network_redirect_uri(), $verifier), true, 302);
    exit;
}

/** GET ?code=&state=: провайдер вернул пользователя — меняем код на токен, проверяем аккаунт, сохраняем ресурс. */
function handle_network_oauth_callback($in) {
    $data = network_oauth_state_take((string)($_GET['state'] ?? ''));
    if ($data === null) network_back(['network_error' => 'Вход устарел: начните подключение заново.']);
    $platform = (string)$data['platform'];
    $network = networks_registry()[$platform] ?? null;
    if ($network === null || isset($_GET['error']) || (string)($_GET['code'] ?? '') === '') network_back(['network_error' => ($network['title'] ?? $platform) . ': вход отменён.']);
    try {
        $secrets = ($network['oauth']['exchange'])((string)$_GET['code'], network_redirect_uri(), (string)$data['verifier']);
        $res = ($network['oauth']['identify'])($secrets) + ['secrets' => $secrets];
        if ($error = network_save_resource((string)$data['uid'], $platform, $network, $res)) network_back(['network_error' => $error]);
    } catch (RuntimeException $e) {
        network_back(['network_error' => $e->getMessage()]);
    }
    network_back(['network' => 'ok', 'platform' => $platform]);
}

/** {platform, fields}: проверить доступ к соцсети, сохранить ресурс (секреты — зашифрованно). Лимит тарифа считает все ресурсы. */
function handle_network_connect($in) {
    $platform = (string)($in['platform'] ?? '');
    $network = networks_registry()[$platform] ?? null;
    if ($platform === 'telegram' || $network === null || empty($network['connect'])) json_error('Эту соцсеть пока нельзя подключить из Posta.', 422);
    if (!empty($network['oauth'])) json_error($network['title'] . ' подключается входом: нажмите «Войти».', 422);
    if ($network['status'] === 'beta' && !radar_is_owner(app_user_id())) json_error($network['title'] . ' в бете: подключает пока только владелец, пока не проверена реальная публикация.', 403);
    if (!in_array($network['status'], ['ready', 'beta'], true)) json_error($network['title'] . ' пока не подключается: ' . $network['note'], 422);
    $fields = array_map(fn($v) => is_string($v) ? trim($v) : '', is_array($in['fields'] ?? null) ? $in['fields'] : []);
    try {
        $res = ($network['connect'])($fields);
    } catch (RuntimeException $e) {
        json_error($e->getMessage(), 422);
    }
    $uid = app_user_id();
    if ($error = network_save_resource($uid, $platform, $network, $res)) json_error($error, 409);
    ok(['channels' => networks_channels_public($uid)]);
}

/** {id}: отключить ресурс соцсети (Telegram — в своём разделе). Посты в очереди у ресурса не дают отключить. */
function handle_network_remove($in) {
    $id = (string)($in['id'] ?? '');
    $channel = channel_get($id);
    if ($channel === null || $channel['platform'] === 'telegram') json_error('Такого ресурса нет.', 404);
    if ($denied = channel_delete_denied($id)) json_error($denied, 409);
    channel_delete($id);
    ok(['channels' => networks_channels_public()]);
}
