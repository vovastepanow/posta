-- Fully assessed public text posts may be delivered before their source is
-- approved. They are not source evidence and never promote the author.
-- The leased public worker is the only writer; final delivery rechecks
-- subscriber demand, overrides, votes, deduplication and display filters.
CREATE TABLE source_autopilot_direct_results (
  scope text NOT NULL CHECK (scope = 'public'),
  topic_key text NOT NULL,
  source_id text NOT NULL,
  post_hash text NOT NULL,
  post jsonb NOT NULL,
  analysis jsonb NOT NULL,
  published_at bigint NOT NULL,
  assessed_at bigint NOT NULL,
  policy_version integer NOT NULL,
  PRIMARY KEY (scope, topic_key, source_id, post_hash)
);
CREATE INDEX source_autopilot_direct_recent
  ON source_autopilot_direct_results (scope, topic_key, published_at DESC);
