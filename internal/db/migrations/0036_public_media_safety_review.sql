-- Independent safety evidence must survive a refresh of unchanged content.
ALTER TABLE public_posts ADD COLUMN media_safety_review jsonb NOT NULL DEFAULT '[]'::jsonb
 CHECK (jsonb_typeof(media_safety_review)='array');

CREATE FUNCTION posta_reset_public_media_safety_review() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF posta_public_revision(OLD.data) IS DISTINCT FROM posta_public_revision(NEW.data) THEN
  NEW.media_safety_review := '[]'::jsonb;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER public_posts_media_safety_revision
 BEFORE UPDATE OF data ON public_posts FOR EACH ROW
 EXECUTE FUNCTION posta_reset_public_media_safety_review();

CREATE FUNCTION posta_invalidate_unsafe_public_media() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF NEW.media_safety_review <> '[]'::jsonb AND NEW.media_safety_review IS DISTINCT FROM OLD.media_safety_review THEN
  DELETE FROM public_post_topics WHERE source_key=NEW.source_key AND post_key=NEW.post_key;
  DELETE FROM public_post_media_observations WHERE source_key=NEW.source_key AND post_key=NEW.post_key;
  DELETE FROM public_post_embeddings WHERE source_key=NEW.source_key AND post_key=NEW.post_key;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER public_posts_media_safety_changed
 AFTER UPDATE OF media_safety_review ON public_posts FOR EACH ROW
 EXECUTE FUNCTION posta_invalidate_unsafe_public_media();
