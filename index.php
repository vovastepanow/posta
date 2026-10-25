<?php
// JSON API интерфейса: ?action=... → обработчик из handlers/. POST требует заголовок X-Posta: 1 (защита от CSRF).
require_once __DIR__ . '/bootstrap.php';
foreach (['content', 'sources', 'account', 'radar', 'networks'] as $file) require_once __DIR__ . "/handlers/$file.php";

function json_response($data, $status = 200): never {
    http_response_code($status);
    header('Content-Type: application/json; charset=utf-8');
    header('Cache-Control: no-store');
    if (is_array($data) && !array_is_list($data) && ai_exchange_log()) $data['ai_log'] = ai_exchange_log();
    echo json_encode($data, JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE);
    exit;
}

function json_error($message, $status = 400): never { json_response(['status' => 'error', 'message' => $message], $status); }
function ok(array $extra = []): never { json_response(['status' => 'success'] + $extra); }

set_exception_handler(function (Throwable $e) {
    error_log(APP_NAME . ' API error: ' . $e);
    // Ожидаемые ошибки (неверный ввод, сбой сети, отказ Telegram) — 400 с текстом, конфликт версий — 409, остальное — 500
    $status = $e instanceof DomainException ? 409 : ($e instanceof InvalidArgumentException || $e instanceof RuntimeException ? 400 : 500);
    json_error(($status === 500 ? 'Внутренняя ошибка сервера: ' : '') . $e->getMessage(), $status);
});

const GET = ['networks_list', 'network_oauth_start', 'network_oauth_callback', 'topic_info', 'vk_oauth_status', 'vk_oauth_start', 'vk_oauth_callback', 'database_options', 'database_read', 'get_current_item', 'settings', 'topics',
    'topic_sources', 'api_token_status', 'discovery_status', 'parser_status', 'model_status', 'account_info', 'telegram_queue', 'proposals', 'proposals_progress', 'topic_record', 'telegram_user_qr_status',
    'telegram_publish_progress', 'telegram_next_slot', 'post_thumb', 'billing_info', 'auth_status', 'login_status', 'radar_access', 'radar', 'channel_style', 'hashtag_suggest', 'calendar', 'outbox_media', 'best_posts', 'mirror_list', 'telegram_video_status', 'video_status', 'get_item_video', 'article_read'];
const POST = ['network_connect', 'network_remove', 'tab_open', 'tab_heartbeat', 'tab_close', 'database_add', 'database_update', 'database_delete', 'database_reset', 'analyze_image', 'vote',
    'refresh_content', 'save_topic', 'topic_parent', 'topic_rubric', 'remove_topic_source', 'topic_reprofile', 'save_second_topic', 'save_query', 'control_search', 'control_sources',
    'app_stop', 'save_filters', 'adult_allow', 'suggest_sources', 'source_vote', 'discover_sources', 'debug_reset', 'model_control', 'telegram_connect', 'telegram_disconnect', 'telegram_channel_default',
    'telegram_publish', 'telegram_cancel', 'telegram_retry', 'telegram_reschedule', 'telegram_duplicate', 'channel_schedule_save', 'channel_hashtags_save', 'channel_style_learn', 'channel_style_save', 'hint_seen', 'telegram_video_fetch', 'video_fetch', 'translate_text', 'ai_caption', 'telegram_user_start', 'telegram_user_confirm', 'telegram_user_qr_start', 'telegram_user_resend',
    'telegram_user_disconnect', 'add_topic_source', 'news_excerpt', 'calendar_fill_plan', 'calendar_fill_apply', 'calendar_check', 'best_posts_take', 'mirror_save', 'mirror_delete', 'own_upload', 'own_import_yadisk', 'own_import_x', 'source_mute', 'topic_format_save', 'own_import_csv', 'api_add_post', 'api_token_create', 'api_token_revoke', 'radar_do', 'proposal_hide', 'proposal_refetch', 'proposal_repair', 'billing_buy', 'referral_bind', 'login_start', 'logout', 'logout_all', 'password_login', 'credentials_save'];
// Псевдонимы: одно действие обслуживает несколько маршрутов
const ROUTE_HANDLERS = ['database_add' => 'database_change', 'database_update' => 'database_change', 'database_delete' => 'database_change',
    'control_search' => 'refresh_content', 'control_sources' => 'discover_sources', 'telegram_publish' => 'publish'];   // старое имя действия — тот же обработчик

$action = (string)($_GET['action'] ?? '');
$method = $_SERVER['REQUEST_METHOD'] ?? 'GET';
$expected = in_array($action, GET, true) ? 'GET' : (in_array($action, POST, true) ? 'POST' : json_error('Неизвестное действие API.', 404));
if ($method !== $expected) { header("Allow: $expected"); json_error("Метод {$method} не поддерживается для этого действия.", 405); }
$input = [];
// Единственное действие без куки и X-Posta: пользователь определяется по токену, а не по сессии сайта
if ($action === 'api_add_post') handle_api_add_post();
if ($method === 'POST') {
    if (($_SERVER['HTTP_X_POSTA'] ?? '') !== '1') json_error('Запрос отклонён: отсутствует заголовок X-Posta.', 403);
    if (str_starts_with(strtolower($_SERVER['CONTENT_TYPE'] ?? ''), 'application/json')) {
        $input = json_decode((string)file_get_contents('php://input'), true);
        if (!is_array($input)) json_error('Тело запроса должно быть JSON-объектом.');
    }
}
if (($_GET['debug'] ?? '') === '1') demo_route($action, $input);   // демо-режим: состояние в PHP-сессии, файлы не меняются
app_user_from_request();
if ($action === 'proposals_progress') handle_proposals_progress();   // опрос каждые полсекунды: без загрузки всей истории
if ($action === 'telegram_publish_progress') handle_telegram_publish_progress();   // опрос во время отправки поста
if ($action === 'login_status') handle_login_status($input);   // опрос статуса входа через Telegram
if ($action === 'password_login') handle_password_login($input);   // вход по логину: анонимному профилю данные не заводим
ensure_default_project_data();
user_touch();
('handle_' . (ROUTE_HANDLERS[$action] ?? $action))($input, $action);
