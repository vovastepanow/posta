-- Реестр проверенных источников: каждый источник, который уже находили, предлагали или отклоняли, — одна строка.
-- Ключ — SourceIDFor(платформа, id). Статус — общий итог по источнику; решения по отдельным темам — в topics (тема → балл, причина, время).
-- Заполняется однократно при старте из каталога, голосов, предложений и старой памяти checked_at (см. source_registry.go).
CREATE TABLE source_registry (
  source_id text PRIMARY KEY,
  platform text NOT NULL DEFAULT '',
  name text NOT NULL DEFAULT '',
  url text NOT NULL DEFAULT '',
  status text NOT NULL,
  reason text NOT NULL DEFAULT '',
  first_seen_at bigint NOT NULL DEFAULT 0,
  checked_at bigint NOT NULL DEFAULT 0,
  check_count integer NOT NULL DEFAULT 0,
  fail_count integer NOT NULL DEFAULT 0,
  topics jsonb NOT NULL DEFAULT '{}',
  metrics jsonb NOT NULL DEFAULT '{}',
  origin jsonb NOT NULL DEFAULT '{}',
  sample jsonb,
  sample_at bigint NOT NULL DEFAULT 0
);

CREATE INDEX source_registry_status ON source_registry (status);
