# 🎯 Умный радар лидов из Telegram

Полная интеграция автоматического обнаружения потенциальных клиентов из Telegram-чатов с использованием локальной модели Ollama.

## 📋 Требования

- ✅ PHP 7.4+
- ✅ MySQL 5.7+ (с поддержкой JSON)
- ✅ Ollama (локальная, на http://127.0.0.1:11434)
- ✅ Установленная модель: `my-model:latest` (или любая другая)
- ✅ PHP-парсер Telegram (MadelineProto, telethon и т.д.)

## 🚀 Быстрый старт

### 1. Создание таблиц БД

Выполните миграцию:

```bash
mysql -u your_user -p your_database < migrations/009_create_telegram_leads.sql
```

Или вручную в phpMyAdmin:
- Скопируйте содержимое `migrations/009_create_telegram_leads.sql`
- Выполните в SQL-редакторе

### 2. Проверка Ollama

```bash
# Проверить, запущена ли Ollama
curl http://127.0.0.1:11434/api/tags

# Установить модель (если не установлена)
ollama pull my-model:latest

# Проверить наличие модели в списке
ollama list | grep my-model
```

### 3. Добавление в Telegram-парсер

Где у вас находится парсер Telegram (например, в `tools/telegram_parser.php`), добавьте при получении нового сообщения:

```php
require_once __DIR__ . '/../lib/ollama_classifier.php';

// Когда получили новое сообщение из чата
$userId = 'some_user_id';
$chatId = 123456789;
$message = 'Ищу решение для автопостинга контента...';
$messageId = 987654321;

// Добавляем в очередь (мгновенно, без ожидания)
addToLeadQueue($userId, $chatId, $message, $messageId);
```

### 4. Запуск воркера

**Вариант 1: Через cron (рекомендуется для production)**

```bash
# Отредактируйте crontab
crontab -e

# Добавьте строку (запуск каждую минуту):
* * * * * /usr/bin/php /var/www/html/postmaker/tools/ollama_worker.php >> /var/log/ollama_worker.log 2>&1

# Или через supervisor (еще лучше для production)
```

**Вариант 2: Как демон (для тестирования)**

```bash
# Запустить в фоне
nohup php /var/www/html/postmaker/tools/ollama_worker.php > /tmp/ollama_worker.log 2>&1 &

# Проверить лог
tail -f /tmp/ollama_worker.log
```

**Вариант 3: Через systemd (самый правильный способ)**

Создайте файл `/etc/systemd/system/ollama-worker.service`:

```ini
[Unit]
Description=Postmaker Ollama Worker
After=network.target

[Service]
Type=simple
User=www-data
WorkingDirectory=/var/www/html/postmaker
ExecStart=/usr/bin/php /var/www/html/postmaker/tools/ollama_worker.php
Restart=always
RestartSec=10
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Включите:
```bash
sudo systemctl daemon-reload
sudo systemctl enable ollama-worker
sudo systemctl start ollama-worker
sudo systemctl status ollama-worker
```

### 5. Проверка на фронтенде

1. Откройте приложение Postmaker
2. Перейдите на вкладку **🎯 Радар лидов**
3. Должны появиться статистика и фильтры
4. Лиды будут появляться автоматически каждые 5 секунд

## 📁 Структура файлов

```
postmaker/
├── lib/
│   └── ollama_classifier.php         # Интеграция с Ollama
├── handlers/
│   └── content.php                   # API эндпоинты (добавлены функции)
├── tools/
│   └── ollama_worker.php             # Фоновый воркер для Ollama
├── js/
│   └── telegram_leads.js             # Фронтенд: Long-Polling + рендеринг
├── css/
│   ├── telegram_leads.css            # Стили карточек лидов
│   └── telegram_leads_panel.css       # Стили основной панели
├── migrations/
│   └── 009_create_telegram_leads.sql # DDL таблиц
└── index.html                        # Добавлена вкладка "Радар лидов"
```

## 🔌 API эндпоинты

### GET /api/telegram_leads
Получить список лидов с фильтром

**Параметры:**
- `limit` (int, default 20, max 100) — количество записей
- `offset` (int, default 0) — смещение для пагинации
- `status` (string, default "new") — фильтр: "new", "replied", "interested", "spam", "ignored"

**Ответ:**
```json
{
  "leads": [
    {
      "id": 123,
      "user_id": "some_user",
      "message": "Ищу решение...",
      "chat_id": 456,
      "lead_status": "new",
      "telegram_username": "myusername",
      "created_at": "2024-01-15 10:30:00"
    }
  ],
  "total": 45,
  "limit": 10,
  "offset": 0
}
```

### GET /api/telegram_leads_stats
Получить статистику лидов

**Ответ:**
```json
{
  "new": 12,
  "interested": 5,
  "replied": 3,
  "spam": 2,
  "ignored": 1,
  "pending_in_queue": 7
}
```

### POST /api/update_telegram_lead
Обновить статус лида

**Тело запроса:**
```json
{
  "lead_id": 123,
  "status": "replied"  // "replied", "interested", "spam", "ignored"
}
```

**Заголовки:**
```
X-Postmaker: 1  (CSRF protection)
Content-Type: application/json
```

## 🛠️ Отладка

### Логи воркера

```bash
# Systemd
sudo journalctl -u ollama-worker -f

# Если через cron
tail -f /var/log/ollama_worker.log

# Если через nohup
tail -f /tmp/ollama_worker.log
```

### Проверить очередь БД

```sql
-- Посмотреть pending сообщения
SELECT COUNT(*) as pending_count FROM telegram_lead_queue 
WHERE status = 'pending';

-- Посмотреть ошибки
SELECT id, ollama_error, attempts FROM telegram_lead_queue 
WHERE status = 'failed' 
ORDER BY created_at DESC LIMIT 10;

-- Посмотреть успешно обработанные лиды
SELECT COUNT(*) as leads_count FROM telegram_leads 
WHERE lead_status = 'new';
```

### Проверить Ollama вручную

```bash
curl -X POST http://127.0.0.1:11434/api/generate \
  -H "Content-Type: application/json" \
  -d '{
    "model": "my-model:latest",
    "prompt": "Ищу решение для автопостинга контента",
    "system": "Ты бинарный классификатор. Если человек ищет софт для автопостинга - верни 1, иначе 0.",
    "stream": false,
    "temperature": 0.1
  }'
```

Ответ должен содержать `"response": "1"` или `"response": "0"`

## ⚙️ Настройка

### Изменить интервал опроса на фронтенде

В `js/telegram_leads.js`, строка инициализации:

```javascript
new TelegramLeadsManager({
    pollInterval: 5000  // Миллисекунды (5 сек)
});
```

### Изменить размер батча в воркере

В `tools/ollama_worker.php`:

```php
const BATCH_SIZE = 5;  // Обрабатывать 5 сообщений за раз
```

### Изменить модель Ollama

В `lib/ollama_classifier.php`:

```php
const OLLAMA_MODEL = 'my-model:latest';  // ← Ваша модель
```

### Изменить системный промпт

В `lib/ollama_classifier.php`, функция `classifyWithOllama()`:

```php
$systemPrompt = <<<PROMPT
Ваш новый промпт здесь...
PROMPT;
```

## 📊 Мониторинг производительности

**Метрики для отслеживания:**

1. **Среднее время обработки сообщения:**
   ```sql
   SELECT AVG(TIMESTAMPDIFF(SECOND, created_at, processed_at)) as avg_seconds
   FROM telegram_lead_queue 
   WHERE status = 'processed';
   ```

2. **Процент успешных обработок:**
   ```sql
   SELECT 
     CONCAT(ROUND(COUNT(CASE WHEN status = 'processed' THEN 1 END) * 100.0 / COUNT(*), 2), '%') as success_rate
   FROM telegram_lead_queue;
   ```

3. **Точность классификации Ollama:**
   Вручную проверьте N случайных лидов, считайте истинные positives:
   ```sql
   SELECT * FROM telegram_leads 
   WHERE lead_status = 'new' 
   ORDER BY RAND() 
   LIMIT 20;
   ```

## 🐛 Возможные проблемы

| Проблема | Решение |
|----------|---------|
| Лиды не появляются | Проверьте, запущен ли воркер (`ps aux \| grep ollama_worker`), есть ли в очереди pending сообщения |
| Ollama ошибка "Connection refused" | Запустите Ollama: `ollama serve`, проверьте http://127.0.0.1:11434/api/tags |
| Воркер зависает | Увеличьте OLLAMA_TIMEOUT в `lib/ollama_classifier.php` |
| Модель возвращает не 0/1 | Проверьте промпт в функции `classifyWithOllama()`, переобучите модель |
| БД ошибка "Table doesn't exist" | Выполните миграцию `migrations/009_create_telegram_leads.sql` |

## 📈 Масштабирование

**Для высоконагруженных сценариев (1000+ сообщений в час):**

1. **Увеличьте BATCH_SIZE** до 10-20 в воркере
2. **Запустите несколько воркеров** параллельно (через supervisor)
3. **Оптимизируйте модель Ollama** (используйте более быструю модель)
4. **Добавьте Redis** для очереди (вместо MySQL)
5. **Кэшируйте результаты** в Redis для одинаковых сообщений

## 📞 Поддержка

При проблемах:
1. Проверьте логи воркера
2. Убедитесь, что Ollama запущена
3. Проверьте статус БД запросами выше
4. Смотрите console браузера на предмет ошибок JavaScript

---

**Версия:** 1.0  
**Последнее обновление:** 2024-01-15
