-- Durable public source inventory, separate from personal pools and history.
CREATE TABLE public_posts (
 source_key text NOT NULL,
 post_key text NOT NULL,
 platform text NOT NULL,
 data jsonb NOT NULL,
 first_seen bigint NOT NULL,
 last_seen bigint NOT NULL,
 PRIMARY KEY (source_key, post_key)
);
CREATE INDEX public_posts_source_recent ON public_posts (source_key, last_seen DESC);
