-- Shadow measurements are isolated from measurements used by active sources.
ALTER TABLE source_autopilot_collection_passes ADD COLUMN scope text NOT NULL DEFAULT 'public';
ALTER TABLE source_autopilot_collection_passes DROP CONSTRAINT source_autopilot_collection_passes_pkey;
ALTER TABLE source_autopilot_collection_passes ADD PRIMARY KEY(scope,topic_key,source_id,slot);

CREATE TABLE source_autopilot_post_evidence (
  scope text NOT NULL,
  topic_key text NOT NULL,
  source_id text NOT NULL,
  post_hash text NOT NULL,
  content_hash text NOT NULL DEFAULT '',
  policy_version integer NOT NULL,
  published_at bigint NOT NULL,
  observed_at bigint NOT NULL,
  relevant boolean NOT NULL,
  eligible boolean NOT NULL,
  advertising boolean NOT NULL,
  PRIMARY KEY(scope,topic_key,source_id,post_hash,policy_version)
);
CREATE INDEX source_autopilot_post_evidence_window ON source_autopilot_post_evidence(scope,topic_key,published_at);
