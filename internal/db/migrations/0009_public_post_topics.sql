ALTER TABLE public_posts ADD COLUMN classification_at bigint NOT NULL DEFAULT 0;
CREATE TABLE public_post_topics (
 source_key text NOT NULL,
 post_key text NOT NULL,
 topic_key text NOT NULL,
 PRIMARY KEY (source_key, post_key, topic_key),
 FOREIGN KEY (source_key, post_key) REFERENCES public_posts(source_key, post_key) ON DELETE CASCADE
);
CREATE INDEX public_post_topics_lookup ON public_post_topics (topic_key);
