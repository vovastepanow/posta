-- Shared public collection is independent from personal feeds and history.
CREATE TABLE topic_collection_queue (
  topic_key text PRIMARY KEY,
  topic text NOT NULL,
  priority integer NOT NULL DEFAULT 0,
  requested_at bigint NOT NULL DEFAULT 0,
  last_done bigint NOT NULL DEFAULT 0,
  lease_until bigint NOT NULL DEFAULT 0,
  lease_id text NOT NULL DEFAULT '',
  attempts integer NOT NULL DEFAULT 0
);
CREATE INDEX topic_collection_queue_ready ON topic_collection_queue (priority DESC, requested_at);
