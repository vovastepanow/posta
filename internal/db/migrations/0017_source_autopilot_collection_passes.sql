-- A successful fetch is evidence of coverage, not proof of relevance.
CREATE TABLE source_autopilot_collection_passes (
  topic_key text NOT NULL,
  source_id text NOT NULL,
  slot bigint NOT NULL,
  fetched_at bigint NOT NULL,
  fetched_count integer NOT NULL CHECK (fetched_count >= 0),
  PRIMARY KEY (topic_key,source_id,slot)
);
CREATE INDEX source_autopilot_collection_passes_window ON source_autopilot_collection_passes(topic_key,source_id,fetched_at);
