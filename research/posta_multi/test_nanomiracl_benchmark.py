"""Offline tests for unbiased full-candidate NanoMIRACL BM25 evaluation."""
import unittest

from nanomiracl_benchmark import BM25Index, available_splits, evaluate_language, \
                                  prepare, validated_language


class NanoTests(unittest.TestCase):
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

    def test_missing_split_fails_closed(self):
        def wrong(_):
            return {"splits":[{"config":"queries","split":"ru","num_examples":200}]}
        with self.assertRaises(ValueError):
            available_splits(wrong)


if __name__ == "__main__":
    unittest.main()
