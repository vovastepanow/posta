-- Restrict legacy account-native video references whose owners are known.
-- Keep access for every account already holding a reference to that file.
WITH records AS MATERIALIZED (
 SELECT user_id,data FROM pool_items
 UNION ALL
 SELECT user_id,data FROM history_items
), names AS (
 SELECT DISTINCT m[1] AS name FROM records r
 CROSS JOIN LATERAL regexp_matches(r.data::text, '"(?:video_local|video)"\s*:\s*"[^"]*/([a-f0-9]{32}\.(?:mp4|webm))"', 'g') m
 WHERE r.data->>'video_via'='tgu' OR r.data->>'fetched_via'='telegram_mtproto'
 OR r.data->'source_attachments' @> '[{"video_via":"tgu"}]'::jsonb
)
INSERT INTO private_media(name,user_id)
SELECT DISTINCT n.name,r.user_id FROM names n JOIN records r ON strpos(r.data::text,n.name)>0
ON CONFLICT DO NOTHING;
