-- Personal history of source recommendations, independent of the shared catalogue.
CREATE TABLE source_impressions (
 user_id text NOT NULL,
 topic_key text NOT NULL,
 source_id text NOT NULL,
 first_shown bigint NOT NULL,
 last_shown bigint NOT NULL,
 show_count integer NOT NULL DEFAULT 1,
 available_after bigint NOT NULL DEFAULT 0,
 decision text NOT NULL DEFAULT '',
 PRIMARY KEY (user_id, topic_key, source_id)
);
CREATE INDEX source_impressions_user_topic ON source_impressions(user_id, topic_key);
