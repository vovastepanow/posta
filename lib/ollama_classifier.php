<?php
/**
 * Интеграция с локальной Ollama для классификации лидов из Telegram
 * Модель: my-model:latest
 */

const OLLAMA_ENDPOINT = 'http://127.0.0.1:11434/api/generate';
const OLLAMA_MODEL = 'my-model:latest';
const OLLAMA_TIMEOUT = 5; // секунд
const OLLAMA_TEMPERATURE = 0.1; // Минимальная случайность для консистентности

/**
 * Классифицировать сообщение из Telegram: это лид или нет?
 *
 * @param string $userMessage Текст сообщения (до 4096 символов)
 * @return array ['success' => bool, 'result' => 0|1|null, 'raw_response' => string, 'error' => string|null]
 */
function classifyWithOllama(string $userMessage): array {
    // Системный промпт на русском: строгий, одно слово на выход
    $systemPrompt = <<<PROMPT
Ты бинарный классификатор лидов для SaaS-сервиса автопостинга контента.

Твоя задача: определить, ищет ли человек в сообщении РЕШЕНИЕ ДЛЯ:
- Автоматизации подбора контента из соцсетей
- Автоматизации постинга контента в свои каналы
- Рерайта или переписывания текстов

Если ДА → верни ТОЛЬКО цифру: 1
Если НЕТ (флуд, реклама других сервисов, общий вопрос) → верни ТОЛЬКО цифру: 0

БЕЗ дополнительного текста, БЕЗ пунктуации, БЕЗ пояснений.
Только "1" или "0".
PROMPT;

    // Подготовка тела запроса для Ollama
    $payload = [
        'model' => OLLAMA_MODEL,
        'system' => $systemPrompt,
        'prompt' => $userMessage,
        'stream' => false,  // КРИТИЧНО! Иначе будет streaming
        'temperature' => OLLAMA_TEMPERATURE,
    ];

    // Настройка cURL
    $ch = curl_init();
    curl_setopt_array($ch, [
        CURLOPT_URL => OLLAMA_ENDPOINT,
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_POST => true,
        CURLOPT_POSTFIELDS => json_encode($payload),
        CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
        CURLOPT_TIMEOUT => OLLAMA_TIMEOUT,
        CURLOPT_CONNECTTIMEOUT => 2,
    ]);

    $response = curl_exec($ch);
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $curlError = curl_error($ch);
    curl_close($ch);

    // Проверка ошибок
    if ($curlError || $httpCode !== 200) {
        return [
            'success' => false,
            'result' => null,
            'raw_response' => '',
            'error' => "Ollama error (HTTP $httpCode): $curlError"
        ];
    }

    // Парсинг ответа от Ollama
    $decoded = json_decode($response, true);
    if (!$decoded) {
        return [
            'success' => false,
            'result' => null,
            'raw_response' => $response,
            'error' => 'Invalid JSON from Ollama'
        ];
    }

    $rawResponse = $decoded['response'] ?? '';

    // Очистка и парсинг бинарного результата
    $cleanResult = extractBinaryResult($rawResponse);

    if ($cleanResult === null) {
        return [
            'success' => false,
            'result' => null,
            'raw_response' => $rawResponse,
            'error' => "Could not parse Ollama response as binary (0 or 1). Got: $rawResponse"
        ];
    }

    return [
        'success' => true,
        'result' => $cleanResult,
        'raw_response' => $rawResponse,
        'error' => null
    ];
}

/**
 * Извлечь из ответа Ollama ровно одно число: 0 или 1
 * Модель может вернуть " 1", "1 ", "\n1", "1." и т.п.
 * Функция максимально толерантна к шумам.
 */
function extractBinaryResult(string $response): ?int {
    // Убираем whitespace с начала и конца
    $cleaned = trim($response);

    // Если первый символ это 0 или 1
    if (preg_match('/^(0|1)/', $cleaned, $m)) {
        return (int)$m[1];
    }

    // Если модель совсем сломалась, возвращаем null
    return null;
}

/**
 * Добавить сообщение в очередь обработки
 * (Вызывается из Telegram-парсера мгновенно)
 */
function addToLeadQueue(
    string $userId,
    int $chatId,
    string $message,
    ?int $messageId = null
): int {
    $pdo = db();

    $stmt = $pdo->prepare("
        INSERT INTO telegram_lead_queue
        (user_id, chat_id, message, message_id, status)
        VALUES (?, ?, ?, ?, 'pending')
    ");

    $stmt->execute([$userId, $chatId, $message, $messageId]);

    return $pdo->lastInsertId();
}

/**
 * Получить количество необработанных лидов в очереди
 */
function getPendingLeadCount(): int {
    $pdo = db();
    $stmt = $pdo->query("SELECT COUNT(*) as cnt FROM telegram_lead_queue WHERE status = 'pending'");
    return (int)$stmt->fetch(PDO::FETCH_ASSOC)['cnt'];
}

/**
 * Получить количество новых лидов (classified = 1)
 */
function getNewLeadsCount(): int {
    $pdo = db();
    $stmt = $pdo->query("SELECT COUNT(*) as cnt FROM telegram_leads WHERE lead_status = 'new'");
    return (int)$stmt->fetch(PDO::FETCH_ASSOC)['cnt'];
}
