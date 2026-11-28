-- Media observations alter the input for semantic retrieval. Fence a vector
-- produced concurrently against the same parent row as content refresh.
ALTER TABLE public_posts ADD COLUMN media_observation_revision bigint NOT NULL DEFAULT 0;
CREATE FUNCTION posta_bump_public_media_embedding_revision() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF TG_OP='UPDATE' AND NEW.description IS NOT DISTINCT FROM OLD.description THEN
  RETURN NEW;
 END IF;
 UPDATE public_posts SET media_observation_revision=media_observation_revision+1
 WHERE source_key=NEW.source_key AND post_key=NEW.post_key;
 DELETE FROM public_post_embeddings WHERE source_key=NEW.source_key AND post_key=NEW.post_key;
 RETURN NEW;
END;
$$;
CREATE TRIGGER public_media_embedding_revision_changed
 AFTER INSERT OR UPDATE OF description ON public_post_media_observations
 FOR EACH ROW EXECUTE FUNCTION posta_bump_public_media_embedding_revision();
-- Invalidate a vector when media was described, including prior descriptions.
UPDATE public_posts p SET media_observation_revision=1
 WHERE EXISTS (SELECT 1 FROM public_post_media_observations m WHERE m.source_key=p.source_key AND m.post_key=p.post_key);
DELETE FROM public_post_embeddings;
