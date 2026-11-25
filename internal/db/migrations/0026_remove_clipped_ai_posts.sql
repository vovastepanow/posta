-- One-time removal of legacy clipped AI prose. Keep user-authored records,
-- rated history, original public posts, source states and publication queues.
-- Remove cached assessments too, so the same clipped prose cannot be redelivered.
CREATE FUNCTION pg_temp.posta_clipped_ai_text(value jsonb) RETURNS boolean
LANGUAGE sql IMMUTABLE AS $$
  SELECT NOT COALESCE(value->'own' = 'true'::jsonb, false)
     AND NOT COALESCE(value->'ai_text_repaired' = 'true'::jsonb, false)
     AND (
       (char_length(btrim(COALESCE(value->>'description', ''))) = 120
         AND rtrim(btrim(value->>'description'), '»”"'' )]}') !~ '[.!?]$')
       OR
       (char_length(btrim(COALESCE(value->>'why_relevant', ''))) BETWEEN 78 AND 80
         AND value->>'why_relevant' NOT LIKE 'Фрагмент публикации:%'
         AND rtrim(btrim(value->>'why_relevant'), '»”"'' )]}') !~ '[.!?]$')
     )
$$;

DELETE FROM pool_items WHERE pg_temp.posta_clipped_ai_text(data);
DELETE FROM analysis_cache WHERE pg_temp.posta_clipped_ai_text(analysis);
DELETE FROM source_autopilot_content_results WHERE pg_temp.posta_clipped_ai_text(analysis);
DELETE FROM source_autopilot_direct_results WHERE pg_temp.posta_clipped_ai_text(analysis);
DROP FUNCTION pg_temp.posta_clipped_ai_text(jsonb);
