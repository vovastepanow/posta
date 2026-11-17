-- Independent discovery jobs; scope is explicit so private work cannot be
-- accidentally merged with public work. Times are UTC Unix seconds.
CREATE TABLE source_autopilot_jobs (
  scope text NOT NULL,
  topic_key text NOT NULL,
  kind text NOT NULL,
  topic text NOT NULL,
  priority integer NOT NULL DEFAULT 0,
  reason text NOT NULL DEFAULT '',
  next_run_at bigint NOT NULL,
  requested_at bigint NOT NULL,
  lease_until bigint NOT NULL DEFAULT 0,
  lease_token text NOT NULL DEFAULT '',
  generation bigint NOT NULL DEFAULT 0,
  attempts integer NOT NULL DEFAULT 0,
  checkpoint jsonb NOT NULL DEFAULT '{}',
  last_error text NOT NULL DEFAULT '',
  enabled boolean NOT NULL DEFAULT true,
  PRIMARY KEY (scope, topic_key, kind)
);
CREATE INDEX source_autopilot_jobs_ready ON source_autopilot_jobs (next_run_at, requested_at) WHERE enabled;
