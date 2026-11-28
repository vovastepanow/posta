-- Public prewarming is not a subscriber account or a taxonomy.
CREATE TABLE public_warm_profiles (
 topic_key text PRIMARY KEY,
 query text NOT NULL,
 target integer NOT NULL DEFAULT 100 CHECK (target BETWEEN 1 AND 1000),
 enabled boolean NOT NULL DEFAULT true,
 start_after bigint NOT NULL DEFAULT 0,
 checked_at bigint NOT NULL DEFAULT 0,
 ready_count integer NOT NULL DEFAULT 0,
 duplicate_count integer NOT NULL DEFAULT 0,
 missing_media_count integer NOT NULL DEFAULT 0,
 diagnosis text NOT NULL DEFAULT 'not_checked'
);
