<?php
// Общий HTTP для адаптеров соцсетей (AUTOPOST_NETWORKS_PROMPT.md, часть B). Ответ: ['status', 'data' (JSON или null), 'raw'].
// Тесты подменяют транспорт: $GLOBALS['network_http_fake'] = fn($method, $url, $headers, $body) => [...] (как outbox_process_due).
// Секреты (токены, пароли) не попадают ни в ошибки, ни в журнал.

/**
 * Адрес, на который можно слать запрос с токеном: только https и публичный адрес (без loopback, частных и служебных сетей).
 * Имя проверяется по всем его адресам, чтобы не обойти фильтр через DNS. Возвращает текст причины или null.
 */
function network_url_unsafe(string $url): ?string {
    $p = parse_url($url);
    if (!is_array($p) || strtolower((string)($p['scheme'] ?? '')) !== 'https' || empty($p['host'])) return 'Нужен адрес https.';
    $host = strtolower(trim((string)$p['host'], '[]'));
    $ips = filter_var($host, FILTER_VALIDATE_IP) ? [$host] : (array)gethostbynamel($host);
    if (!$ips) return 'Адрес не найден.';
    foreach ($ips as $ip) {
        if (filter_var($ip, FILTER_VALIDATE_IP, FILTER_FLAG_NO_PRIV_RANGE | FILTER_FLAG_NO_RES_RANGE) === false) return 'Адрес указывает во внутреннюю сеть сервера.';
    }
    return null;
}

/** Отказ соцсети, который повтор не исправит: неверный токен, запрещённый текст, неподдерживаемый файл. Очередь сразу ставит пост в «неудачные». */
class NetworkRejectedError extends RuntimeException {}

/** Коды ответа, после которых повтор бессмыслен (запрос надо менять или доступ чинить). */
const NETWORK_REJECT_STATUSES = [400, 401, 403, 404, 410, 422];

/** Запрос к API соцсети. $body — строка, массив (multipart с CURLFile) или null. */
function network_http(string $method, string $url, array $headers = [], $body = null): array {
    if (isset($GLOBALS['network_http_fake'])) return ($GLOBALS['network_http_fake'])($method, $url, $headers, $body);
    if ($reason = network_url_unsafe($url)) throw new NetworkRejectedError('Запрос отклонён: ' . $reason);
    $ch = curl_init($url);
    $options = [CURLOPT_CUSTOMREQUEST => $method, CURLOPT_RETURNTRANSFER => true, CURLOPT_HTTPHEADER => $headers, CURLOPT_CONNECTTIMEOUT => 10,
        CURLOPT_TIMEOUT => 60, CURLOPT_USERAGENT => HTTP_UA, CURLOPT_SSL_VERIFYPEER => true, CURLOPT_SSL_VERIFYHOST => 2,
        CURLOPT_PROTOCOLS => CURLPROTO_HTTPS, CURLOPT_REDIR_PROTOCOLS => CURLPROTO_HTTPS, CURLOPT_FOLLOWLOCATION => false];
    if ($body !== null) $options[CURLOPT_POSTFIELDS] = $body;
    curl_setopt_array($ch, $options);
    $resp_headers = [];
    curl_setopt($ch, CURLOPT_HEADERFUNCTION, function ($ch, $line) use (&$resp_headers) {
        if (str_contains($line, ':')) { [$k, $v] = explode(':', $line, 2); $resp_headers[strtolower(trim($k))] = trim($v); }
        return strlen($line);
    });
    $raw = curl_exec($ch);
    $status = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $error = curl_error($ch);
    curl_close($ch);
    if ($raw === false) throw new RuntimeException('Нет связи с сервисом' . ($error !== '' ? ': ' . $error : '') . '.');
    return ['status' => $status, 'data' => json_decode((string)$raw, true), 'raw' => (string)$raw, 'headers' => $resp_headers];
}

/** Проверка ответа: данные при ожидаемом коде, иначе исключение с текстом ошибки сервиса (без секретов). */
function network_expect(array $r, array $statuses, string $what): array {
    if (in_array($r['status'], $statuses, true) && is_array($r['data'])) return $r['data'];
    if (in_array($r['status'], $statuses, true)) return [];
    $d = is_array($r['data']) ? $r['data'] : [];
    $error = $d['error_description'] ?? $d['error'] ?? $d['error_msg'] ?? $d['message'] ?? $d['detail'] ?? '';
    if (is_array($error)) $error = $error['message'] ?? $error['error_msg'] ?? '';
    $message = $what . ': ' . ((string)$error !== '' ? mb_substr((string)$error, 0, 200) : 'код ' . $r['status']) . '.';
    throw in_array($r['status'], NETWORK_REJECT_STATUSES, true) ? new NetworkRejectedError($message) : new RuntimeException($message);
}

/** Файлы поста, которые соцсеть примет: по виду и размеру (AUTOPOST_NETWORKS_PROMPT.md, часть A.4). Возвращает [оставлено, пропущено (тексты)]. */
function network_fit_media(array $network, array $media): array {
    $kept = [];
    $skipped = [];
    foreach ($media as $f) {
        $kind = (string)($f['kind'] ?? '');
        $name = $kind === 'video' ? 'видео' : ($kind === 'animation' ? 'GIF' : 'картинка');
        if (!in_array($kind, $network['kinds'] ?? [], true)) { $skipped[] = "{$name}: {$network['title']} такой вид не принимает"; continue; }
        $size = is_file((string)($f['path'] ?? '')) ? (int)filesize($f['path']) : 0;
        $max = $network['max_bytes'][$kind] ?? null;
        if ($max !== null && $size > $max) { $skipped[] = "{$name}: больше лимита {$network['title']}"; continue; }
        if (count($kept) >= (int)($network['media_max'] ?? 1)) { $skipped[] = "{$name}: больше " . ($network['media_max'] ?? 1) . " файлов для {$network['title']}"; continue; }
        $kept[] = $f;
    }
    return [$kept, $skipped];
}
