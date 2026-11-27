-- Restrict account-owned image cache entries before their HTTP access.
-- No cascading owner deletion: an orphaned private file must not become public.
CREATE TABLE private_media (
 name text NOT NULL,
 user_id text NOT NULL,
 PRIMARY KEY (name,user_id)
);

-- Restore known ownership for legacy personal uploads and account-specific
-- Telegram history. Extract generated image cache names from these records,
-- including the original album attachments.
INSERT INTO private_media(name,user_id)
SELECT DISTINCT m[1], r.user_id
FROM (
 SELECT user_id,data FROM pool_items
 UNION ALL
 SELECT user_id,data FROM history_items
) r
CROSS JOIN LATERAL regexp_matches(r.data::text, '"(?:image_local|image)"\s*:\s*"[^"]*/([a-f0-9]{32}\.(?:jpg|jpeg|png|gif|webp|bmp))"', 'g') m
WHERE r.data->>'source_key' = 'own:' || r.user_id OR r.data->>'fetched_via' = 'telegram_mtproto'
ON CONFLICT DO NOTHING;
