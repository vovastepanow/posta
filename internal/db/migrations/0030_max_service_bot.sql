-- Shared MAX assistant: bindings are independent of publication credentials.
CREATE TABLE max_bot_links (
  user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  bot_id bigint NOT NULL CHECK (bot_id > 0),
  max_user_id bigint NOT NULL CHECK (max_user_id > 0),
  linked_at bigint NOT NULL,
  UNIQUE (bot_id, max_user_id)
);

CREATE TABLE max_bot_link_requests (
  user_id text PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  bot_id bigint NOT NULL CHECK (bot_id > 0),
  token_hash text NOT NULL UNIQUE,
  created_at bigint NOT NULL,
  max_user_id bigint,
  attempts integer NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5)
);
CREATE INDEX max_bot_link_requests_created ON max_bot_link_requests(created_at);
