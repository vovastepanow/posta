-- Shared lexical search independent of legacy topic labels.
CREATE FUNCTION posta_public_search_vector(doc jsonb) RETURNS tsvector
LANGUAGE sql IMMUTABLE PARALLEL SAFE AS $$
 SELECT to_tsvector('pg_catalog.simple'::regconfig, body)
     || to_tsvector('pg_catalog.russian'::regconfig, body)
     || to_tsvector('pg_catalog.english'::regconfig, body)
 FROM (SELECT coalesce(doc->>'title','') || ' ' || coalesce(doc->>'text','')
             || ' ' || coalesce(doc->>'thread_title','') AS body) AS t
$$;
CREATE INDEX public_posts_search_gin ON public_posts USING gin (posta_public_search_vector(data));
