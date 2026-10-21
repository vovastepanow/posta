#!/usr/bin/env php
<?php
/**
 * tools/ollama_worker.php
 *
 * Фоновый воркер для обработки очереди сообщений из Telegram через Ollama
 *
 * Запуск:
 * - Через cron: */1 * * * * /usr/bin/php /var/www/html/postmaker/tools/ollama_worker.php
 * - Как daemon: nohup php /var/www/html/postmaker/tools/ollama_worker.php > /tmp/ollama_worker.log 2>&1 &
 * - Через supervisor (рекомендуется для production)
 */

require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../lib/ollama_classifier.php';

const BATCH_SIZE = 5; // За раз берём 5 сообщений
const SLEEP_EMPTY = 5; // Спим 5 сек если очередь пуста
const SLEEP_BETWEEN_BATCHES = 200000; // 200ms между батчами (microseconds)
const MAX_ATTEMPTS = 3; // Максимум попыток для каждого сообщения

/**
 * Основной цикл обработки очереди
 */
function processQueue() {
    error_log('[Ollama Worker] Started at ' . date('Y-m-d H:i:s'));

    while (true) {
        try {
            processNextBatch();
        } catch (Throwable $e) {
            error_log("[Ollama Worker] Fatal error: " . $e->getMessage());
            // Продолжаем работу, не падаем
        }

        usleep(SLEEP_BETWEEN_BATCHES);
    }
}

/**
 * Обработать батч из BATCH_SIZE сообщений
 */
function processNextBatch(): void {
    $pdo = db();

    // Берём до BATCH_SIZE pending сообщений, отсортированные по времени добавления
    $stmt = $pdo->prepare("
        SELECT id, message, user_id, chat_id, message_id, attempts
        FROM telegram_lead_queue
        WHERE status = 'pending'
        ORDER BY created_at ASC
        LIMIT ?
    ");
    $stmt->execute([BATCH_SIZE]);
    $items = $stmt->fetchAll(PDO::FETCH_ASSOC);

    if (empty($items)) {
        // Очередь пуста, спим и проверяем снова
        sleep(SLEEP_EMPTY);
        return;
    }

    error_log('[Ollama Worker] Processing batch of ' . count($items) . ' items');

    foreach ($items as $item) {
        processSingleItem($pdo, $item);
    }
}

/**
 * Обработать одно сообщение
 */
function processSingleItem(PDO $pdo, array $item): void {
    $queueId = $item['id'];
    $message = $item['message'];
    $userId = $item['user_id'];
    $chatId = $item['chat_id'];
    $messageId = $item['message_id'];
    $attempts = $item['attempts'];

    try {
        // Помечаем как "обрабатывается"
        $pdo->prepare("
            UPDATE telegram_lead_queue
            SET status = 'processing'
            WHERE id = ?
        ")->execute([$queueId]);

        error_log("[Queue ID: $queueId] Sending to Ollama...");

        // Отправляем в Ollama
        $result = classifyWithOllama($message);

        if (!$result['success']) {
            // Ошибка при обработке
            $error = $result['error'];
            error_log("[Queue ID: $queueId] Ollama error: $error");

            $newAttempts = $attempts + 1;

            // Обновляем статус
            $pdo->prepare("
                UPDATE telegram_lead_queue
                SET status = 'failed', ollama_error = ?, attempts = ?
                WHERE id = ?
            ")->execute([$error, $newAttempts, $queueId]);

            return;
        }

        // Успешно получили результат
        $ollamaResult = $result['result']; // 0 или 1
        error_log("[Queue ID: $queueId] Ollama result: $ollamaResult");

        // Обновляем запись в очереди
        $pdo->prepare("
            UPDATE telegram_lead_queue
            SET status = 'processed', ollama_result = ?, processed_at = NOW()
            WHERE id = ?
        ")->execute([$ollamaResult, $queueId]);

        // Если это лид (результат = 1), создаём запись в telegram_leads
        if ($ollamaResult === 1) {
            error_log("[Queue ID: $queueId] Creating lead record...");

            $telegram_username = extractUsernameFromMessage($message);
            $telegram_profile_url = "https://t.me/$userId";

            $pdo->prepare("
                INSERT INTO telegram_leads
                (queue_id, user_id, chat_id, message, message_id, lead_status,
                 telegram_username, telegram_profile_url, confidence_score)
                VALUES (?, ?, ?, ?, ?, 'new', ?, ?, 1.00)
            ")->execute([
                $queueId,
                $userId,
                $chatId,
                $message,
                $messageId,
                $telegram_username,
                $telegram_profile_url
            ]);

            error_log("[Queue ID: $queueId] Lead record created");
        }

    } catch (Throwable $e) {
        error_log("[Queue ID: $queueId] Exception: " . $e->getMessage());

        $pdo->prepare("
            UPDATE telegram_lead_queue
            SET status = 'failed', ollama_error = ?, attempts = attempts + 1
            WHERE id = ?
        ")->execute(["Exception: " . $e->getMessage(), $queueId]);
    }
}

/**
 * Извлечь username из текста сообщения (если есть упоминание @username)
 * Это примерная реализация; может быть улучшена
 */
function extractUsernameFromMessage(string $message): ?string {
    if (preg_match('/@([a-zA-Z0-9_]{5,32})/', $message, $m)) {
        return $m[1];
    }
    return null;
}

// Запуск воркера
processQueue();
