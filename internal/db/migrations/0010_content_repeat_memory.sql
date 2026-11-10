-- Compact personal duplicate memory; media and post bodies are not stored here.
CREATE TABLE content_repeat_memory (
 user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 kind text NOT NULL CHECK (kind IN ('key', 'phash')),
 fingerprint text NOT NULL,
 expires_at bigint NOT NULL,
 PRIMARY KEY (user_id, kind, fingerprint)
);
CREATE INDEX content_repeat_memory_expiry ON content_repeat_memory(expires_at);
