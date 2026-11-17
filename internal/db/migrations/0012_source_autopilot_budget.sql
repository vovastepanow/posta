-- A reservation is charged before an external call. An interrupted call stays
-- charged because its actual completion cannot be inferred after a crash.
CREATE TABLE source_autopilot_budget (
  scope text NOT NULL,
  resource text NOT NULL,
  day date NOT NULL,
  used bigint NOT NULL DEFAULT 0 CHECK (used >= 0),
  PRIMARY KEY (scope, resource, day)
);
CREATE TABLE source_autopilot_reservations (
  reservation_id text PRIMARY KEY,
  scope text NOT NULL,
  resource text NOT NULL,
  day date NOT NULL,
  amount bigint NOT NULL CHECK (amount > 0),
  created_at timestamptz NOT NULL DEFAULT now()
);
