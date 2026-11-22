"""Offline tests for unbiased full-candidate NanoMIRACL BM25 evaluation."""
import unittest

from nanomiracl_benchmark import BM25Index, available_splits, evaluate_language, \
                                  prepare, validated_language


class NanoTests(unittest.TestCase):
    def test_fetch_retries_rate_limit_but_not_other_client_errors(self):
        import io
        import urllib.error
        from unittest.mock import patch
        from nanomiracl_benchmark import fetch_json
        response = io.BytesIO(b'{"ok":true}')
        response.headers = {}
        rate_limit = urllib.error.HTTPError('https://example.org', 429, 'rate limit',
                                           {'Retry-After': '3'}, None)
        with patch('nanomiracl_benchmark.urllib.request.urlopen',
                   side_effect=[rate_limit, response]) as request, \
             patch('nanomiracl_benchmark.time.sleep') as sleep:
            self.assertEqual(fetch_json('https://example.org'), {'ok': True})
            self.assertEqual(request.call_count, 2)
            sleep.assert_called_once_with(3)
        forbidden = urllib.error.HTTPError('https://example.org', 403, 'forbidden', {}, None)
        with patch('nanomiracl_benchmark.urllib.request.urlopen', side_effect=forbidden), \
             patch('nanomiracl_benchmark.time.sleep') as sleep:
            with self.assertRaises(urllib.error.HTTPError):
                fetch_json('https://example.org')
            sleep.assert_not_called()

    def test_inverted_bm25_ranks_match(self):
        index = BM25Index({"a": "красный спортивный автомобиль",
                           "b": "суп вкусный суп",
                           "c": "красный автомобиль мощный"})
        ranked = [index.ids[i] for i in index.search("красный автомобиль")]
        self.assertEqual(ranked[0], "a")
        items = evaluate_language({"q": "красный автомобиль"},
                                  {"a":"красный спортивный автомобиль",
                                   "b":"суп вкусный суп",
                                   "c":"красный автомобиль мощный"},
                                  {"q":{"a"}})
        self.assertEqual(items[0]["recall_1"], 1)

    def test_unverified_qrels_and_duplicates_rejected(self):
        queries = [{"_id":"q","text":"test query"}]
        corpus = [{"_id":"d","text":"test document"}]
        labels = [{"query-id":"q","corpus-id":"d"}]
        q, c, r = validated_language(
            [{"_id":str(i),"text":f"query {i}"} for i in range(200)],
            corpus, [{"query-id":str(i),"corpus-id":"d"} for i in range(200)], "ru")
        self.assertEqual(len(q), 200)
        with self.assertRaises(ValueError):
            validated_language(queries, corpus, [{"query-id":"q","corpus-id":"missing"}], "ru")
        with self.assertRaises(ValueError):
            validated_language(queries, corpus+corpus, labels, "ru")

    def test_split_schema_and_full_end_to_end_with_no_network(self):
        data = {}
        for lang in ("ru","en"):
            for kind in ("queries","corpus","qrels"):
                data[(kind,lang)] = []
            for i in range(200):
                data[("queries",lang)].append({"_id":str(i),"text":f"topic {i} {lang}"})
                data[("qrels",lang)].append({"query-id":str(i),"corpus-id":str(i)})
            for i in range(400):
                data[("corpus",lang)].append({"_id":str(i),"text":f"topic {i} {lang} article information"})

        def fetch(url):
            if "/api/datasets/" in url:
                return {"sha":"a"*40,"private":False,"gated":False}
            if "/splits" in url:
                return {"splits":[{"config":kind,"split":lang,
                                   "num_examples":len(rows)}
                                  for (kind,lang), rows in data.items()]}
            from urllib.parse import parse_qs, urlparse
            q=parse_qs(urlparse(url).query)
            kind, lang=q["config"][0],q["split"][0]
            offset,limit=int(q["offset"][0]),int(q["length"][0])
            records=data[(kind,lang)]
            return {"num_rows_total":len(records),
                    "rows":[{"row":r} for r in records[offset:offset+limit]]}

        report = prepare(fetch)
        self.assertEqual(report["overall"]["queries"], 400)
        self.assertEqual(report["candidate_corpora"]["ru"]["documents"], 400)
        self.assertEqual(report["source_bm25_candidates_used"], False)
        self.assertEqual(len(report["source_content_sha256"]),64)
        self.assertGreater(report["languages"]["ru"]["recall_5"],0)
        self.assertNotIn("topic 1",str(report))


    def test_reject_mismatched_existing_snapshot_before_write(self):
        from pathlib import Path
        import tempfile
        from nanomiracl_benchmark import prepare
        from urllib.parse import urlparse, parse_qs
        rows={}
        for lang in ("ru", "en"):
            rows[("queries",lang)]=[
                {"_id":str(i),"text":f"question {i} {lang}"} for i in range(200)]
            rows[("qrels",lang)]=[
                {"query-id":str(i),"corpus-id":str(i)} for i in range(200)]
            rows[("corpus",lang)]=[
                {"_id":str(i),"text":f"document {i} {lang}"} for i in range(400)]
        def fetch(url):
            if "/api/datasets/" in url:
                return {"sha":"d"*40,"gated":False,"private":False}
            if "/splits" in url:
                return {"splits":[{"config":kind,"split":lang} for kind,lang in rows]}
            query=parse_qs(urlparse(url).query)
            r=rows[(query["config"][0],query["split"][0])]
            off,n=int(query["offset"][0]),int(query["length"][0])
            return {"num_rows_total":len(r),
                    "rows":[{"row":x} for x in r[off:off+n]]}
        with tempfile.TemporaryDirectory() as directory:
            destination=Path(directory)/"frozen.json"
            with self.assertRaisesRegex(ValueError, "differs from frozen"):
                prepare(fetch, freeze_data=destination, expected_content_sha="0"*64)
            self.assertFalse(destination.exists())

    def test_missing_split_fails_closed(self):
        def wrong(_):
            return {"splits":[{"config":"queries","split":"ru","num_examples":200}]}
        with self.assertRaises(ValueError):
            available_splits(wrong)


if __name__ == "__main__":
    unittest.main()



class NanoFrozenSnapshotTests(unittest.TestCase):
    def test_freeze_snapshot_no_overwrite_and_checksum(self):
        import tempfile
        import hashlib
        import json
        from pathlib import Path
        from urllib.parse import parse_qs, urlparse
        from nanomiracl_benchmark import prepare

        data = {}
        for language in ("ru", "en"):
            data[("queries", language)] = [
                {"_id":str(i), "text":f"topic {i} {language}"} for i in range(200)]
            data[("qrels", language)] = [
                {"query-id":str(i), "corpus-id":str(i)} for i in range(200)]
            data[("corpus", language)] = [
                {"_id":str(i), "text":f"topic {i} {language} example"}
                for i in range(400)]

        def fetch(url):
            if "/api/datasets/" in url:
                return {"sha": "b"*40, "private":False,"gated":False}
            if "/splits" in url:
                return {"splits":[{"config":k, "split":lang}
                                  for (k,lang) in data]}
            params=parse_qs(urlparse(url).query)
            values=data[(params["config"][0],params["split"][0])]
            start,limit=int(params["offset"][0]),int(params["length"][0])
            return {"num_rows_total":len(values),
                    "rows":[{"row":v} for v in values[start:start+limit]]}

        with tempfile.TemporaryDirectory() as temp:
            path=Path(temp)/"nano-private.json"
            report=prepare(fetch, freeze_data=path)
            contents=path.read_bytes()
            self.assertEqual(hashlib.sha256(contents).hexdigest(),
                             report["frozen_file_sha256"])
            self.assertEqual(report["overall"]["queries"],400)
            self.assertEqual(json.loads(contents)["format"],
                             "posta-nanomiracl-frozen-evaluation-v1")
            with self.assertRaises(FileExistsError):
                prepare(fetch, freeze_data=path)
