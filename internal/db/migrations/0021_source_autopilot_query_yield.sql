CREATE TABLE source_autopilot_query_sources (
  scope text NOT NULL,
  topic_key text NOT NULL,
  query text NOT NULL,
  source_id text NOT NULL,
  discovered_at bigint NOT NULL,
  PRIMARY KEY (scope, topic_key, query, source_id)
);
CREATE INDEX source_autopilot_query_yield ON source_autopilot_query_sources(scope, topic_key, source_id);
