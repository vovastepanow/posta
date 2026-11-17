-- Source state is scoped to the complete topic profile and access domain.
CREATE TABLE source_autopilot_sources (
  scope text NOT NULL,
  topic_key text NOT NULL,
  source_id text NOT NULL,
  state text NOT NULL CHECK (state IN ('candidate','probation','active','cooldown','rejected')),
  resume_state text NOT NULL DEFAULT '',
  reason text NOT NULL DEFAULT '',
  evidence jsonb NOT NULL DEFAULT '{}',
  policy_version integer NOT NULL,
  first_seen_at bigint NOT NULL,
  checked_at bigint NOT NULL DEFAULT 0,
  next_check_at bigint NOT NULL DEFAULT 0,
  PRIMARY KEY (scope,topic_key,source_id),
  CHECK ((state='cooldown' AND resume_state IN ('candidate','probation','active','rejected')) OR (state<>'cooldown' AND resume_state=''))
);
CREATE INDEX source_autopilot_sources_review ON source_autopilot_sources(scope,topic_key,next_check_at);

-- Overrides are separate from automatic content assessments. Empty owner is
-- reserved for a global override; user overrides never change public state.
CREATE TABLE source_autopilot_overrides (
  scope text NOT NULL,
  owner_id text NOT NULL,
  topic_key text NOT NULL,
  source_id text NOT NULL,
  decision text NOT NULL CHECK (decision IN ('pinned','excluded')),
  updated_at bigint NOT NULL,
  PRIMARY KEY (scope,owner_id,topic_key,source_id)
);

CREATE TABLE source_autopilot_decisions (
  id bigserial PRIMARY KEY,
  scope text NOT NULL,
  topic_key text NOT NULL,
  source_id text NOT NULL,
  old_state text NOT NULL,
  new_state text NOT NULL,
  reason text NOT NULL,
  evidence jsonb NOT NULL DEFAULT '{}',
  policy_version integer NOT NULL,
  at bigint NOT NULL
);
CREATE INDEX source_autopilot_decisions_lookup ON source_autopilot_decisions(scope,topic_key,id DESC);
