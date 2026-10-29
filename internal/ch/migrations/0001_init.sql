-- Posta: события и аналитика в ClickHouse. Только дописываются; читаются агрегатами (админка, отчёты качества).
-- Запросы разделяются строкой «;» в конце, мигратор выполняет их по одному.

CREATE TABLE IF NOT EXISTS ai_calls (
  ts DateTime64(3),
  provider LowCardinality(String),
  model LowCardinality(String),
  role LowCardinality(String),
  user_id String,
  topic String,
  ok UInt8,
  error String,
  images UInt8,
  prompt_tokens UInt32,
  completion_tokens UInt32,
  cached_tokens UInt32,
  cost_usd Float64,
  latency_ms UInt32
) ENGINE = MergeTree ORDER BY (ts, role) TTL toDateTime(ts) + INTERVAL 365 DAY;

CREATE TABLE IF NOT EXISTS votes (
  ts DateTime64(3),
  user_id String,
  topic String,
  vote LowCardinality(String),
  reason LowCardinality(String),
  source_key String,
  platform LowCardinality(String),
  relevance Int16,
  card_score Int16,
  format LowCardinality(String),
  tags Array(String),
  image_key String
) ENGINE = MergeTree ORDER BY (topic, ts);

CREATE TABLE IF NOT EXISTS screening (
  ts DateTime64(3),
  user_id String,
  topic String,
  stage LowCardinality(String),
  source_key String,
  platform LowCardinality(String),
  image_key String,
  score Int16,
  passed UInt8,
  reason String,
  model LowCardinality(String)
) ENGINE = MergeTree ORDER BY (topic, ts) TTL toDateTime(ts) + INTERVAL 180 DAY;

CREATE TABLE IF NOT EXISTS parser_log (
  ts DateTime64(3),
  user_id String,
  line String
) ENGINE = MergeTree ORDER BY (user_id, ts) TTL toDateTime(ts) + INTERVAL 30 DAY;

CREATE TABLE IF NOT EXISTS publish_events (
  ts DateTime64(3),
  user_id String,
  channel_id String,
  platform LowCardinality(String),
  ok UInt8,
  error String,
  post_ref String,
  media_count UInt8,
  scheduled UInt8
) ENGINE = MergeTree ORDER BY (user_id, ts);
