-- Only public, completed text analyses are retained for per-owner admission.
CREATE TABLE source_autopilot_text_results (
  scope text NOT NULL,
  topic_key text NOT NULL,
  source_id text NOT NULL,
  post_hash text NOT NULL,
  post jsonb NOT NULL,
  analysis jsonb NOT NULL,
  published_at bigint NOT NULL,
  assessed_at bigint NOT NULL,
  PRIMARY KEY(scope, topic_key, source_id, post_hash)
);
CREATE INDEX source_autopilot_text_results_recent ON source_autopilot_text_results(scope,topic_key,published_at DESC);
