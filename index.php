<?php
// JSON API интерфейса: ?action=... → обработчик из handlers/. POST требует заголовок X-Postmaker: 1 (защита от CSRF).
require_once __DIR__ . '/bootstrap.php';
foreach (['content', 'sources', 'account'] as $file) require_once __DIR__ . "/handlers/$file.php";

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

const GET = ['vk_oauth_status', 'vk_oauth_start', 'vk_oauth_callback', 'database_options', 'database_read', 'get_current_item', 'settings', 'topics',
    'topic_sources', 'discovery_status', 'parser_status', 'model_status', 'account_info', 'telegram_queue', 'proposals', 'proposals_progress', 'topic_record', 'telegram_user_qr_status',
    'telegram_publish_progress', 'telegram_leads', 'telegram_leads_stats'];
const POST = ['tab_open', 'tab_heartbeat', 'tab_close', 'database_add', 'database_update', 'database_delete', 'database_reset', 'analyze_image', 'vote',
    'refresh_content', 'save_topic', 'topic_parent', 'remove_topic_source', 'topic_reprofile', 'save_second_topic', 'control_search', 'control_sources',
    'app_stop', 'save_filters', 'suggest_sources', 'source_vote', 'discover_sources', 'debug_reset', 'model_control', 'telegram_connect', 'telegram_disconnect',
    'telegram_publish', 'telegram_cancel', 'telegram_user_start', 'telegram_user_confirm', 'telegram_user_qr_start', 'telegram_user_resend',
    'telegram_user_disconnect', 'proposal_hide', 'update_telegram_lead'];
// Псевдонимы: одно действие обслуживает несколько маршрутов
const ROUTE_HANDLERS = ['database_add' => 'database_change', 'database_update' => 'database_change', 'database_delete' => 'database_change',
    'control_search' => 'refresh_content', 'control_sources' => 'discover_sources'];

$action = (string)($_GET['action'] ?? '');
$method = $_SERVER['REQUEST_METHOD'] ?? 'GET';
$expected = in_array($action, GET, true) ? 'GET' : (in_array($action, POST, true) ? 'POST' : json_error('Неизвестное действие API.', 404));
if ($method !== $expected) { header("Allow: $expected"); json_error("Метод {$method} не поддерживается для этого действия.", 405); }
$input = [];
if ($method === 'POST') {
    if (($_SERVER['HTTP_X_POSTMAKER'] ?? '') !== '1') json_error('Запрос отклонён: отсутствует заголовок X-Postmaker.', 403);
    if (str_starts_with(strtolower($_SERVER['CONTENT_TYPE'] ?? ''), 'application/json')) {
        $input = json_decode((string)file_get_contents('php://input'), true);
        if (!is_array($input)) json_error('Тело запроса должно быть JSON-объектом.');
    }
}
if (($_GET['debug'] ?? '') === '1') demo_route($action, $input);   // демо-режим: состояние в PHP-сессии, файлы не меняются
app_user_from_request();
if ($action === 'proposals_progress') handle_proposals_progress();   // опрос каждые полсекунды: без загрузки всей истории
if ($action === 'telegram_publish_progress') handle_telegram_publish_progress();   // опрос во время отправки поста
ensure_default_project_data();
user_touch();
('handle_' . (ROUTE_HANDLERS[$action] ?? $action))($input, $action);
