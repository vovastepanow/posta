-- Индекс качества источника: лайки и дизлайки всех пользователей (жалоба «ошибка подбора» — дизлайк), по ключу источника записи.
-- quality (0–100) пересчитывается при каждой оценке; начальное значение 50.
CREATE TABLE source_quality (
  source_key text PRIMARY KEY,
  likes integer NOT NULL DEFAULT 0,
  dislikes integer NOT NULL DEFAULT 0,
  quality real NOT NULL DEFAULT 50,
  updated_at bigint NOT NULL DEFAULT 0
);

-- Ключ треда имиджборда («4chan:b:123», «dvach:b:123») — доской: оценивается источник, а не тред.
INSERT INTO source_quality (source_key, likes, dislikes, quality, updated_at)
SELECT k, likes, dislikes, round((100.0 * (likes + 2) / (likes + dislikes + 4))::numeric, 1), extract(epoch FROM now())::bigint
FROM (
  SELECT regexp_replace(data->>'source_key', '^((4chan|dvach):[^:]+):.*$', '\1') AS k,
         count(*) FILTER (WHERE collection = 'liked') AS likes,
         count(*) FILTER (WHERE collection = 'disliked' OR (collection = 'complaints' AND data->>'complaint_reason' = 'match_error')) AS dislikes
  FROM history_items
  WHERE collection IN ('liked', 'disliked', 'complaints') AND COALESCE(data->>'source_key', '') <> '' AND data->>'source_key' NOT LIKE 'own:%'
  GROUP BY 1
) t WHERE likes + dislikes > 0;
