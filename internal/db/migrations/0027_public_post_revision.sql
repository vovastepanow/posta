-- Invalidate topic labels in the same transaction as a changed public post.
-- Counters do not change the subject or the original media.
CREATE FUNCTION posta_public_revision(value jsonb) RETURNS jsonb
LANGUAGE sql IMMUTABLE STRICT AS $$
 SELECT value - ARRAY['engagement','views','likes','comments','reshares','reactions','shares','view_count','like_count','comment_count','repost_count','score','previous_complete_snapshot']::text[]
$$;

CREATE FUNCTION posta_invalidate_public_topics() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF posta_public_revision(OLD.data) IS DISTINCT FROM posta_public_revision(NEW.data) THEN
  DELETE FROM public_post_topics WHERE source_key=OLD.source_key AND post_key=OLD.post_key;
  NEW.classification_at := 0;
 END IF;
 RETURN NEW;
END;
$$;

CREATE TRIGGER public_posts_revision_changed
 BEFORE UPDATE OF data ON public_posts FOR EACH ROW
 EXECUTE FUNCTION posta_invalidate_public_topics();

-- Support bounded garbage collection of the derived public inventory.
CREATE INDEX public_posts_last_seen ON public_posts(last_seen);
