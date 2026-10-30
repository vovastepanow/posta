-- «Не знаю» — пол-лайка (лайк с весом 0,5): (лайки + 0,5·не знаю + 2) / (лайки + 0,5·не знаю + дизлайки + 4).
ALTER TABLE source_quality ADD COLUMN maybes integer NOT NULL DEFAULT 0;

INSERT INTO source_quality (source_key, likes, dislikes, maybes, quality, updated_at)
SELECT k, likes, dislikes, maybes, round((100.0 * (likes + 0.5 * maybes + 2) / (likes + 0.5 * maybes + dislikes + 4))::numeric, 1), extract(epoch FROM now())::bigint
FROM (
  SELECT regexp_replace(data->>'source_key', '^((4chan|dvach):[^:]+):.*$', '\1') AS k,
         count(*) FILTER (WHERE collection = 'liked') AS likes,
         count(*) FILTER (WHERE collection = 'disliked' OR (collection = 'complaints' AND data->>'complaint_reason' = 'match_error')) AS dislikes,
         count(*) FILTER (WHERE collection = 'undecided') AS maybes
  FROM history_items
  WHERE collection IN ('liked', 'disliked', 'undecided', 'complaints') AND COALESCE(data->>'source_key', '') <> '' AND data->>'source_key' NOT LIKE 'own:%'
  GROUP BY 1
) t WHERE likes + dislikes + maybes > 0
ON CONFLICT (source_key) DO UPDATE SET likes = EXCLUDED.likes, dislikes = EXCLUDED.dislikes, maybes = EXCLUDED.maybes, quality = EXCLUDED.quality, updated_at = EXCLUDED.updated_at;
