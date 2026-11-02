-- Posta: начальная схема Postgres (перенос со SQLite, версия 18). Время — секунды Unix (bigint), как и раньше:
-- предметная логика сравнивает их с time.Now().Unix(). Записи с меняющимся набором полей — JSONB.

CREATE TABLE users (
  id text PRIMARY KEY,
  created_at bigint NOT NULL,
  last_seen bigint NOT NULL DEFAULT 0,
  proposals_checked_at bigint NOT NULL DEFAULT 0,
  threads_checked_at bigint NOT NULL DEFAULT 0,
  settings jsonb NOT NULL DEFAULT '{}',
  connections jsonb NOT NULL DEFAULT '{}',
  ref_code text UNIQUE,
  referred_by text NOT NULL DEFAULT '',
  telegram_id bigint UNIQUE,
  tg_username text NOT NULL DEFAULT '',
  tg_name text NOT NULL DEFAULT '',
  adult_allowed boolean NOT NULL DEFAULT false,
  login text UNIQUE,
  password_hash text NOT NULL DEFAULT ''
);
CREATE INDEX users_referred_by ON users(referred_by);
CREATE INDEX users_last_seen ON users(last_seen);

-- Пул: записи, отобранные сборщиком и ещё не оценённые пользователем
CREATE TABLE pool_items (
  id bigserial PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  position integer NOT NULL,
  image_key text NOT NULL,
  topic text NOT NULL DEFAULT '',
  data jsonb NOT NULL
);
CREATE INDEX pool_items_user ON pool_items(user_id, position);
CREATE INDEX pool_items_key ON pool_items(image_key);

-- История оценок: liked, disliked, undecided, complaints
CREATE TABLE history_items (
  id bigserial PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  collection text NOT NULL,
  position integer NOT NULL,
  image_key text NOT NULL,
  proposal_hidden boolean NOT NULL DEFAULT false,
  data jsonb NOT NULL
);
CREATE INDEX history_items_user ON history_items(user_id, collection, position);
CREATE INDEX history_items_key ON history_items(image_key);

CREATE TABLE outbox (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  publish_at bigint NOT NULL,
  status text NOT NULL,
  data jsonb NOT NULL
);
CREATE INDEX outbox_user ON outbox(user_id, publish_at);
CREATE INDEX outbox_due ON outbox(status, publish_at);

CREATE TABLE published (
  id bigserial PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at bigint NOT NULL,
  data jsonb NOT NULL
);
CREATE INDEX published_user ON published(user_id, id DESC);

-- Каталог источников: kind — telegram_channels, vk_publics, rss_feeds, ok_groups, tiktok_accounts, 2ch_boards…
CREATE TABLE catalog_sources (
  kind text NOT NULL,
  source_id text NOT NULL,
  position integer NOT NULL,
  data jsonb NOT NULL,
  PRIMARY KEY (kind, source_id)
);

CREATE TABLE source_votes (
  id bigserial PRIMARY KEY,
  source_id text NOT NULL,
  position integer NOT NULL,
  data jsonb NOT NULL
);
CREATE INDEX source_votes_source ON source_votes(source_id);

CREATE TABLE source_suggestions (
  id bigserial PRIMARY KEY,
  source_id text NOT NULL,
  position integer NOT NULL,
  data jsonb NOT NULL
);
CREATE INDEX source_suggestions_source ON source_suggestions(source_id);

CREATE TABLE topics (
  id text PRIMARY KEY,
  position integer NOT NULL,
  data jsonb NOT NULL
);

-- Состояние приложения (флаги админки, квоты, задания). Значение — сам JSON, без обёртки {"_kv_value": …}.
-- expires_at > 0 — ключ временный (квоты, счётчики), уборка раз в час удаляет просроченные.
CREATE TABLE kv (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  expires_at bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX kv_expires ON kv(expires_at) WHERE expires_at > 0;

-- Дорогие кэши, которые стоит пережить перезапуск: переводы, статьи, оценки тредов, история каналов.
-- Сырые ответы площадок (треды Двача, 4chan, ленты) сюда НЕ кладутся: они живут в памяти процесса минуты.
-- В SQLite такие ответы копились в kv без срока и занимали 96% базы.
CREATE TABLE cache (
  key text PRIMARY KEY,
  value jsonb NOT NULL,
  expires_at bigint NOT NULL
);
CREATE INDEX cache_expires ON cache(expires_at);

CREATE TABLE analysis_cache (
  key text PRIMARY KEY,
  analysis jsonb NOT NULL,
  at bigint NOT NULL
);
CREATE INDEX analysis_cache_at ON analysis_cache(at);

CREATE TABLE screened_cache (
  url text PRIMARY KEY,
  topic text NOT NULL,
  source text NOT NULL DEFAULT '',
  score integer NOT NULL,
  phash text NOT NULL DEFAULT '',
  at bigint NOT NULL,
  ver integer NOT NULL DEFAULT 1
);
CREATE INDEX screened_cache_at ON screened_cache(at);
CREATE INDEX screened_cache_topic ON screened_cache(lower(topic));

CREATE TABLE source_verdicts (
  topic text NOT NULL,
  source text NOT NULL,
  passed integer NOT NULL DEFAULT 0,
  total integer NOT NULL DEFAULT 0,
  at bigint NOT NULL,
  PRIMARY KEY (topic, source)
);

-- Деньги: копейки
CREATE TABLE ledger (
  id bigserial PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at bigint NOT NULL,
  amount bigint NOT NULL,
  kind text NOT NULL,
  note text NOT NULL DEFAULT '',
  related_user text NOT NULL DEFAULT ''
);
CREATE INDEX ledger_user ON ledger(user_id, id);

CREATE TABLE subscriptions (
  user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  plan text NOT NULL,
  channels integer NOT NULL,
  posts_per_day integer NOT NULL,
  price bigint NOT NULL,
  started_at bigint NOT NULL,
  until bigint NOT NULL
);

CREATE TABLE post_usage (
  id bigserial PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at bigint NOT NULL
);
CREATE INDEX post_usage_user ON post_usage(user_id, at);

CREATE TABLE sessions (
  token_hash text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at bigint NOT NULL,
  last_seen bigint NOT NULL
);
CREATE INDEX sessions_user ON sessions(user_id);

CREATE TABLE login_requests (
  token_hash text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at bigint NOT NULL,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'asked', 'confirmed', 'used')),
  chat_id bigint,
  telegram_id bigint,
  tg_username text NOT NULL DEFAULT '',
  tg_name text NOT NULL DEFAULT ''
);

CREATE TABLE login_failures (
  key text NOT NULL,
  at bigint NOT NULL
);
CREATE INDEX login_failures_key ON login_failures(key, at);

-- Ресурсы соцсетей пользователя. Telegram: ref = chat_id. Секреты — secrets_enc (XChaCha20-Poly1305 с привязкой к записи).
CREATE TABLE channels (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform text NOT NULL DEFAULT 'telegram',
  ref text NOT NULL DEFAULT '',
  secrets_enc text NOT NULL DEFAULT '',
  meta jsonb NOT NULL DEFAULT '{}',
  position integer NOT NULL DEFAULT 0,
  token_enc text NOT NULL DEFAULT '',
  bot_id bigint NOT NULL DEFAULT 0,
  bot_username text NOT NULL DEFAULT '',
  chat_id bigint NOT NULL DEFAULT 0,
  chat_title text NOT NULL DEFAULT '',
  chat_username text NOT NULL DEFAULT '',
  chat_type text NOT NULL DEFAULT '',
  connected_at bigint NOT NULL,
  schedule jsonb NOT NULL DEFAULT '{}',
  style jsonb NOT NULL DEFAULT '{}',
  hashtags text NOT NULL DEFAULT 'off',
  UNIQUE (user_id, platform, ref)
);
CREATE INDEX channels_user ON channels(user_id, position);

CREATE TABLE mirrors (
  id text PRIMARY KEY,
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform text NOT NULL,
  source_id text NOT NULL,
  name text NOT NULL DEFAULT '',
  channel_id text,
  mode text NOT NULL DEFAULT 'pool',
  rules jsonb NOT NULL DEFAULT '{}',
  last_post_key jsonb NOT NULL DEFAULT '[]',
  last_checked_at bigint NOT NULL DEFAULT 0,
  created_at bigint NOT NULL
);
CREATE INDEX mirrors_user ON mirrors(user_id);

-- Радар лидов
CREATE TABLE telegram_lead_queue (
  id bigserial PRIMARY KEY,
  user_id text NOT NULL,
  chat_id bigint NOT NULL,
  message text NOT NULL,
  message_id bigint,
  status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'processed', 'failed')),
  ollama_result integer,
  ollama_error text,
  ollama_reason text,
  created_at timestamptz NOT NULL DEFAULT now(),
  processed_at timestamptz,
  attempts integer NOT NULL DEFAULT 0,
  chat_ref text,
  chat_title text,
  chat_username text,
  sender_id bigint,
  sender_username text,
  sender_name text,
  message_date timestamptz
);
CREATE INDEX telegram_lead_queue_status ON telegram_lead_queue(status);
CREATE INDEX telegram_lead_queue_chat ON telegram_lead_queue(chat_id, created_at);
CREATE UNIQUE INDEX telegram_lead_queue_unique ON telegram_lead_queue(user_id, chat_id, message_id);

CREATE TABLE telegram_leads (
  id bigserial PRIMARY KEY,
  queue_id bigint NOT NULL UNIQUE REFERENCES telegram_lead_queue(id) ON DELETE CASCADE,
  user_id text NOT NULL,
  chat_id bigint NOT NULL,
  message text,
  message_id bigint,
  lead_status text NOT NULL DEFAULT 'new' CHECK (lead_status IN ('new', 'replied', 'spam', 'interested', 'ignored')),
  telegram_username text,
  telegram_profile_url text,
  confidence_score real NOT NULL DEFAULT 1.0,
  assigned_to bigint,
  created_at timestamptz NOT NULL DEFAULT now(),
  replied_at timestamptz,
  updated_at timestamptz NOT NULL DEFAULT now(),
  chat_title text,
  sender_id bigint,
  sender_name text,
  message_url text,
  reason text
);
CREATE INDEX telegram_leads_status ON telegram_leads(lead_status, created_at);
CREATE INDEX telegram_leads_user ON telegram_leads(user_id);
