<?php
// Админка радара лидов (только владелец): без action — страница, с action — JSON. Подробности — lib/radar_admin.php.
require __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../lib/radar_admin.php';

header('X-Frame-Options: DENY');
header('Referrer-Policy: no-referrer');
header('Cache-Control: no-store');
header("Content-Security-Policy: default-src 'self'; style-src 'self'; script-src 'self'; img-src 'self' data:");

function admin_json($data, $status = 200): never {
    http_response_code($status);
    header('Content-Type: application/json; charset=utf-8');
    echo json_encode($data, JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE);
    exit;
}

$action = (string)($_GET['action'] ?? '');
if ($action === '') { admin_page(admin_password_hash() !== null); exit; }

$method = $_SERVER['REQUEST_METHOD'] ?? 'GET';
$expected = in_array($action, ADMIN_GET, true) ? 'GET' : (in_array($action, ADMIN_POST, true) ? 'POST' : admin_json(['status' => 'error', 'message' => 'Неизвестное действие.'], 404));
if ($method !== $expected) { header("Allow: $expected"); admin_json(['status' => 'error', 'message' => 'Метод не поддерживается.'], 405); }

try {
    admin_session_start();
    if ($method === 'POST' && !admin_csrf_ok()) throw new AdminHttpError('Запрос отклонён: нет токена X-Admin-CSRF.', 403);
    if (!in_array($action, ['session', 'login'], true) && !admin_is_logged()) throw new AdminHttpError('Нужен вход.', 401);
    $input = $method === 'POST' ? (json_decode((string)file_get_contents('php://input'), true) ?? []) : $_GET;
    if (!is_array($input)) throw new AdminHttpError('Тело запроса должно быть JSON-объектом.');
    if (!in_array($action, ['login', 'logout'], true)) session_write_close();   // долгие запросы не держат блокировку сессии
    admin_json(['status' => 'success'] + ('admin_api_' . $action)($input));
} catch (AdminHttpError $e) {
    admin_json(['status' => 'error', 'message' => $e->getMessage()], $e->status);
} catch (RuntimeException $e) {
    admin_json(['status' => 'error', 'message' => $e->getMessage()], 400);
} catch (Throwable $e) {
    error_log(APP_NAME . ' admin error: ' . $e);
    admin_json(['status' => 'error', 'message' => 'Внутренняя ошибка сервера.'], 500);
}

/** HTML страницы: без инлайн-JS и стилей (CSP), всё в radar.js и radar.css. */
function admin_page(bool $configured) { ?>
<!doctype html>
<html lang="ru">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>Радар лидов · Postå</title>
<link rel="stylesheet" href="radar.css">
</head>
<body>
<?php if (!$configured): ?>
<main class="wrap"><p class="note">Задайте пароль: <code>php tools/cli.php admin-password</code></p></main>
<?php else: ?>
<main class="wrap">
<header class="top">
    <h1>Радар лидов · Postå</h1>
    <button type="button" id="logout" class="btn" hidden>Выйти</button>
</header>

<form id="login" class="card" hidden>
    <h2>Вход</h2>
    <label>Пароль владельца <input type="password" id="login-password" autocomplete="current-password" required></label>
    <button type="submit" class="btn primary">Войти</button>
    <p id="login-error" class="error" role="alert"></p>
</form>

<div id="app" hidden>
    <p id="flash" class="error" role="alert"></p>

    <section class="card" id="account-card">
        <h2>Аккаунт</h2>
        <p id="account-empty" class="note" hidden>Нет аккаунта Telegram: подключите его в приложении → Личный кабинет.</p>
        <div id="account-body">
            <label>Аккаунт Telegram, через который работает радар <select id="account"></select></label>
            <label class="check"><input type="checkbox" id="enabled"> Радар включён</label>
            <p id="radar-state" class="note"></p>
        </div>
    </section>

    <section class="card" id="chats-card">
        <h2>Чаты</h2>
        <p id="chats-empty" class="note" hidden>Добавьте чаты: нажмите «Найти чаты».</p>
        <ul id="chats" class="list"></ul>
        <form id="chat-add" class="row">
            <input type="text" id="chat-ref" placeholder="@чат" maxlength="40" aria-label="Добавить @чат">
            <button type="submit" class="btn">Добавить</button>
        </form>
        <button type="button" id="find-toggle" class="btn primary">Найти чаты</button>
        <div id="find" hidden>
            <label>Запросы поиска (по одному в строке) <textarea id="queries" rows="6"></textarea></label>
            <button type="button" id="find-run" class="btn">Искать</button>
            <p id="find-state" class="note"></p>
            <ul id="found" class="list"></ul>
            <button type="button" id="found-add" class="btn primary" hidden>Добавить отмеченные</button>
        </div>
    </section>

    <section class="card" id="scan-card">
        <h2>Проверка</h2>
        <button type="button" id="scan" class="btn primary">Проверить сейчас</button>
        <pre id="log" class="log" aria-live="polite"></pre>
    </section>

    <section class="card" id="leads-card">
        <h2>Лиды</h2>
        <p id="stats" class="note"></p>
        <div id="filters" class="row filters"></div>
        <p id="leads-empty" class="note" hidden></p>
        <div id="leads"></div>
        <button type="button" id="leads-more" class="btn" hidden>Показать ещё</button>
    </section>

    <details class="card" id="rejected-card">
        <summary>Отсеяно моделью</summary>
        <p class="note">Последние сообщения, которые модель не сочла лидами. Если среди них есть нужные — настройте отсев.</p>
        <p id="rejected-empty" class="note" hidden>Пока ничего не отсеяно.</p>
        <div id="rejected"></div>
    </details>
</div>
</main>
<script src="radar.js"></script>
<?php endif; ?>
</body>
</html>
<?php }
