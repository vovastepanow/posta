-- A demand belongs to a user, independently of the topic selected on screen.
CREATE TABLE source_autopilot_demands (
  user_id text NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  topic_key text NOT NULL,
  topic text NOT NULL,
  mode text NOT NULL CHECK (mode IN ('off', 'shadow', 'auto')),
  posts_per_day double precision NOT NULL CHECK (posts_per_day >= 0 AND posts_per_day <= 1000),
  horizon_days double precision NOT NULL CHECK (horizon_days > 0 AND horizon_days <= 365),
  updated_at bigint NOT NULL,
  PRIMARY KEY (user_id, topic_key)
);
