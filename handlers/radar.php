<?php
// Вкладка «Радар» — только владелец: GET radar?op=…, POST radar_do {op, …}. Логика — lib/radar_admin.php.

require_once __DIR__ . '/../lib/radar_admin.php';

/** Видна ли вкладка: спрашивает каждая страница при загрузке. */
function handle_radar_access() { json_response(['owner' => radar_is_owner()]); }

function handle_radar() { radar_dispatch((string)($_GET['op'] ?? ''), ADMIN_GET, $_GET); }

function handle_radar_do($in) { radar_dispatch((string)($in['op'] ?? ''), ADMIN_POST, (array)$in); }

/** Не владельцу — 404, как будто радара нет. */
function radar_dispatch(string $op, array $allowed, array $input) {
    if (!radar_is_owner()) json_error('Неизвестное действие API.', 404);
    if (!in_array($op, $allowed, true)) json_error('Неизвестная операция радара.', 404);
    session_write_close();
    try { ok(('admin_api_' . $op)($input)); }
    catch (AdminHttpError $e) { json_error($e->getMessage(), $e->status); }
}
