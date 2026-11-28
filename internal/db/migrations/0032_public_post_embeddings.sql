-- Versioned, normalized public text embeddings. No mandatory extension:
-- deployments without pgvector keep exact cosine retrieval and FTS.
CREATE TABLE public_post_embeddings (
 source_key text NOT NULL,
 post_key text NOT NULL,
 model text NOT NULL,
 embedding real[] NOT NULL CHECK (cardinality(embedding)=512),
 indexed_at bigint NOT NULL,
 PRIMARY KEY (source_key,post_key,model),
 FOREIGN KEY (source_key,post_key) REFERENCES public_posts(source_key,post_key) ON DELETE CASCADE
);
CREATE INDEX public_post_embeddings_model ON public_post_embeddings(model);
CREATE FUNCTION posta_public_embedding_dot(a real[], b real[]) RETURNS double precision
LANGUAGE sql IMMUTABLE STRICT PARALLEL SAFE AS $$
 SELECT sum(x::double precision*y::double precision) FROM unnest(a,b) AS v(x,y)
$$;
CREATE FUNCTION posta_invalidate_public_embeddings() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
 IF posta_public_revision(OLD.data) IS DISTINCT FROM posta_public_revision(NEW.data) THEN
  DELETE FROM public_post_embeddings WHERE source_key=OLD.source_key AND post_key=OLD.post_key;
 END IF;
 RETURN NEW;
END;
$$;
CREATE TRIGGER public_posts_embedding_revision_changed
 BEFORE UPDATE OF data ON public_posts FOR EACH ROW
 EXECUTE FUNCTION posta_invalidate_public_embeddings();

-- Installation may require a package and DBA privileges. An optional failure
-- must not prevent starting Posta or rolling forward the other migrations.
DO $$
BEGIN
 IF EXISTS (SELECT 1 FROM pg_available_extensions WHERE name='vector') THEN
  CREATE EXTENSION IF NOT EXISTS vector;
  EXECUTE 'CREATE INDEX public_post_embeddings_hnsw ON public_post_embeddings USING hnsw ((embedding::vector(512)) vector_cosine_ops)';
 END IF;
EXCEPTION WHEN OTHERS THEN
 RAISE NOTICE 'Optional pgvector index unavailable: %', SQLERRM;
END;
$$;
