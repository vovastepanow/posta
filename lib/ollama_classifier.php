<?php
/**
 * Классификация сообщений из Telegram: являются ли они лидами для Postä
 * Использует локальную Ollama модель (SMART_MODEL) с JSON-схемой
 */

const LEAD_MODEL = SMART_MODEL;
const LEAD_TIMEOUT = 120; // 120 секунд на ответ модели
const LEAD_BATCH = 15;    // максимум 15 сообщений за проход

const LEAD_KEYWORDS = [
    'контент', 'пост', 'автопост', 'отложен', 'расписан', 'канал', 'паблик', 'группу вк',
    'админ', 'smm', 'смм', 'наполн', 'мем', 'рерайт', 'бот'
];

/**
 * Классифицировать сообщение: это лид или нет?
 * Возвращает ['lead' => 0|1, 'reason' => string] или выбрасывает RuntimeException
 */
function lead_classify(string $text): array {
    if (!parser_session_alive()) {
        // В CLI парсер может быть неживой — используем прямой запрос без проверки сессии
        return lead_classify_direct($text);
    }

    // В веб-запросе используем ollama_quick_json (проверяет сессию)
    $format = [
        'type' => 'object',
        'properties' => [
            'lead' => ['type' => 'integer', 'enum' => [0, 1]],
            'reason' => ['type' => 'string']
        ],
        'required' => ['lead', 'reason']
    ];

    $prompt = lead_classify_prompt($text);

    try {
        $result = ollama_quick_json(LEAD_MODEL, $prompt, $format, 60, LEAD_TIMEOUT);
        if (!is_array($result) || !isset($result['lead'])) {
            throw new RuntimeException('Модель не ответила корректным JSON');
        }
        return [
            'lead' => (int)$result['lead'],
            'reason' => substr((string)($result['reason'] ?? ''), 0, 100)
        ];
    } catch (Exception $e) {
        throw new RuntimeException('Модель не ответила: ' . $e->getMessage());
    }
}

/**
 * Прямой запрос к Ollama без проверки сессии (для CLI)
 */
function lead_classify_direct(string $text): array {
    $prompt = lead_classify_prompt($text);
    $format = [
        'type' => 'object',
        'properties' => [
            'lead' => ['type' => 'integer', 'enum' => [0, 1]],
            'reason' => ['type' => 'string']
        ],
        'required' => ['lead', 'reason']
    ];

    $payload = [
        'model' => LEAD_MODEL,
        'prompt' => $prompt,
        'format' => $format,
        'stream' => false,
        'think' => false,
        'options' => ['temperature' => 0, 'num_predict' => 60],
        'keep_alive' => OLLAMA_KEEP_ALIVE
    ];

    $ch = curl_init(OLLAMA_GENERATE_URL);
    curl_setopt_array($ch, [
        CURLOPT_RETURNTRANSFER => true,
        CURLOPT_POST => true,
        CURLOPT_POSTFIELDS => json_encode($payload),
        CURLOPT_HTTPHEADER => ['Content-Type: application/json'],
        CURLOPT_TIMEOUT => LEAD_TIMEOUT,
        CURLOPT_CONNECTTIMEOUT => 5
    ]);

    $response = curl_exec($ch);
    $httpCode = curl_getinfo($ch, CURLINFO_HTTP_CODE);
    $curlError = curl_error($ch);
    curl_close($ch);

    if ($curlError || $httpCode !== 200) {
        throw new RuntimeException("HTTP $httpCode: $curlError");
    }

    $decoded = json_decode($response, true);
    if (!$decoded || empty($decoded['response'])) {
        throw new RuntimeException('Пустой ответ от модели');
    }

    $json = decode_model_json($decoded['response']);
    if (!is_array($json) || !isset($json['lead'])) {
        throw new RuntimeException('Модель вернула некорректный JSON');
    }

    return [
        'lead' => (int)$json['lead'],
        'reason' => substr((string)($json['reason'] ?? ''), 0, 100)
    ];
}

/**
 * Дешёвый отсев до модели: проверяет длину, ключевые слова, признаки запроса
 */
function lead_prefilter(string $text): bool {
    $text = trim($text);
    $len = mb_strlen($text);

    // Проверка длины
    if ($len < 15 || $len > 1500) {
        return false;
    }

    // Проверка ключевых слов
    $lower = mb_strtolower($text);
    $has_keyword = false;
    foreach (LEAD_KEYWORDS as $keyword) {
        if (str_contains($lower, $keyword)) {
            $has_keyword = true;
            break;
        }
    }

    if (!$has_keyword) {
        return false;
    }

    // Проверка признаков запроса (вопрос, просьба или поиск)
    return (bool)preg_match('/[?]|ищу|посоветуй|подскажи|нужен|нужна|кто знает|как |где |чем |помогите|помощь/ui', $text);
}

/**
 * Промпт для классификации (полный текст включая инструкции)
 */
function lead_classify_prompt(string $text): string {
    $text = mb_substr($text, 0, 1500);
    return <<<PROMPT
Ты отбираешь потенциальных клиентов для сервиса Postä. Сервис сам находит свежий контент по теме (посты, мемы, видео из Telegram, VK, форумов) и публикует его в канал владельца по расписанию.

Лид (1) — автор САМ ищет такое решение или жалуется на эту боль: ищет, где брать контент для канала, чем автоматизировать постинг, как вести канал без затрат времени, просит посоветовать сервис/бота для отложенных постов или подбора контента, ищет админа/контент-менеджера для наполнения канала.
Не лид (0) — реклама своих услуг (SMM-специалист предлагает себя), продажа каналов, спам, флуд, вопросы не про контент и постинг, сообщения без запроса.

Примеры:
«Посоветуйте бота, чтобы посты сами выходили по расписанию» → 1
«Где брать мемы для паблика каждый день? Устал искать вручную» → 1
«Ищу человека, который будет наполнять мой телеграм-канал про авто» → 1
«Веду ваши соцсети под ключ, пишите в лс» → 0
«Продам канал 5к подписчиков» → 0
«Кто-нибудь знает, когда обновят тарифы?» → 0

Сообщение:
"""$text"""

Ответь JSON: {"lead": 0 или 1, "reason": "до 12 слов, почему"}
PROMPT;
}
