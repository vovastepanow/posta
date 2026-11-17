-- Personal admission is never promoted to shared topic evidence.
CREATE TABLE source_autopilot_admissions (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  topic_key text NOT NULL,
  source_key text NOT NULL,
  post_hash text NOT NULL,
  policy_version integer NOT NULL,
  accepted boolean NOT NULL,
  observed_at bigint NOT NULL,
  PRIMARY KEY (user_id, topic_key, source_key, post_hash, policy_version)
);
CREATE INDEX source_autopilot_admissions_window ON source_autopilot_admissions(user_id, topic_key, observed_at);
