-- Preserve the owner-specific personal-blog setting for the historical
-- stored query "SMM и блогинг"; it must not silently affect a new "smm" query.
DO $$
DECLARE
 old_key text := 'v7:' || encode(sha256(convert_to('smm','UTF8')),'hex');
 new_key text := 'v7:' || encode(sha256(convert_to('smm и блогинг','UTF8')),'hex');
BEGIN
 UPDATE users SET settings=jsonb_set(settings,'{personal_blog_modes}',
  CASE WHEN (settings->'personal_blog_modes') ? new_key THEN
   (settings->'personal_blog_modes') - old_key
  ELSE
   ((settings->'personal_blog_modes') - old_key) ||
    jsonb_build_object(new_key,settings->'personal_blog_modes'->old_key)
  END)
 WHERE jsonb_typeof(settings->'personal_blog_modes')='object'
  AND (settings->'personal_blog_modes') ? old_key;
END $$;
