-- Reuse neutral media descriptions as retrieval evidence, never as acceptance.
CREATE TABLE public_post_media_observations (
 source_key text NOT NULL,
 post_key text NOT NULL,
 description text NOT NULL CHECK (length(btrim(description)) > 0),
 observed_at bigint NOT NULL,
 PRIMARY KEY (source_key,post_key),
 FOREIGN KEY (source_key,post_key) REFERENCES public_posts(source_key,post_key) ON DELETE CASCADE
);
CREATE FUNCTION posta_public_media_vector(body text) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT to_tsvector('pg_catalog.simple'::regconfig,body)
     || to_tsvector('pg_catalog.russian'::regconfig,body)
     || to_tsvector('pg_catalog.english'::regconfig,body)
$$;
CREATE INDEX public_post_media_search_gin ON public_post_media_observations
 USING gin (posta_public_media_vector(description));
CREATE FUNCTION posta_invalidate_public_media_observation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF posta_public_revision(OLD.data) IS DISTINCT FROM posta_public_revision(NEW.data) THEN
  DELETE FROM public_post_media_observations WHERE source_key=OLD.source_key AND post_key=OLD.post_key;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER public_posts_media_revision_changed
 BEFORE UPDATE OF data ON public_posts FOR EACH ROW
 EXECUTE FUNCTION posta_invalidate_public_media_observation();
