-- Таблица очереди сообщений из Telegram для обработки Ollama
CREATE TABLE IF NOT EXISTS telegram_lead_queue (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  user_id VARCHAR(64) NOT NULL,
  chat_id BIGINT NOT NULL,
  message LONGTEXT NOT NULL COLLATE utf8mb4_unicode_ci,
  message_id BIGINT,
  status ENUM('pending', 'processing', 'processed', 'failed') DEFAULT 'pending',
  ollama_result TINYINT(1) DEFAULT NULL,
  ollama_error TEXT,
  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  processed_at TIMESTAMP NULL,
  attempts INT DEFAULT 0,

  INDEX idx_status (status),
  INDEX idx_created (created_at),
  INDEX idx_chat (chat_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- Таблица подтвержденных лидов (результат Ollama = 1)
CREATE TABLE IF NOT EXISTS telegram_leads (
  id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  queue_id BIGINT UNSIGNED NOT NULL UNIQUE,
  FOREIGN KEY (queue_id) REFERENCES telegram_lead_queue(id) ON DELETE CASCADE,

  user_id VARCHAR(64) NOT NULL,
  chat_id BIGINT NOT NULL,
  message LONGTEXT COLLATE utf8mb4_unicode_ci,
  message_id BIGINT,

  lead_status ENUM('new', 'replied', 'spam', 'interested', 'ignored') DEFAULT 'new',
  telegram_username VARCHAR(255),
  telegram_profile_url VARCHAR(512),

  confidence_score DECIMAL(3, 2) DEFAULT 1.00,
  assigned_to INT,

  created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
  replied_at TIMESTAMP NULL,
  updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,

  INDEX idx_status (lead_status),
  INDEX idx_created (created_at),
  INDEX idx_user (user_id),
  INDEX idx_queue (queue_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
