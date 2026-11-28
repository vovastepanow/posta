-- Historically the exact title "SMM и блогинг" and all its aliases shared
-- the digest of "smm". The stored title is authoritative: move that former
-- identity away from the newly independent literal request "smm".
-- Other stored canonical titles already use their literal digest.
DO $$
DECLARE
 old_key text := 'v7:' || encode(sha256(convert_to('smm','UTF8')),'hex');
 new_key text := 'v7:' || encode(sha256(convert_to('smm и блогинг','UTF8')),'hex');
 table_name text;
BEGIN
 FOREACH table_name IN ARRAY ARRAY[
  'source_autopilot_jobs','source_autopilot_demands',
  'source_autopilot_sources','source_autopilot_overrides',
  'source_autopilot_decisions','source_autopilot_admissions',
  'source_autopilot_collection_passes','source_autopilot_post_evidence',
  'source_autopilot_query_sources','source_autopilot_content_results',
  'source_autopilot_direct_results','public_warm_profiles'
 ] LOOP
  EXECUTE format('UPDATE %I SET topic_key=$1 WHERE topic_key=$2',table_name)
   USING new_key,old_key;
 END LOOP;
END $$;
