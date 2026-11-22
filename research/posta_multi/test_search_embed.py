"""Offline tests for Posta Embed's read-only hybrid search CLI ranking."""
import unittest

from search_embed import post_link, select_rankings, useful_text


def row(id, text, source="a", url=None):
    return {
        "id": id, "source_key": source, "platform": "Telegram",
        "data": {"text": text, "url": url or "https://example.com/" + id},
    }


class SearchEmbedTests(unittest.TestCase):
    def test_real_query_keeps_neural_signal_and_drops_placeholders(self):
        records = [
            row("placeholder", "Пост без текста"),
            row("finance", "Что такое дивиденды и акции", "bank"),
            row("other", "Рыжий кот спит на диване", "pets"),
        ]
        results = select_rankings(
            records, [.99, .91, .7],
            query="Дивиденды", mode="text", limit=10
        )
        ids = [r["post_id"] for r in results]
        self.assertNotIn("placeholder", ids)
        self.assertEqual(ids[0], "finance")
        self.assertEqual(len(ids), 2)

    def test_lexical_boost_and_diversity(self):
        records = [
            row("a", "новости транспорта", "source1"),
            row("b", "новости транспорта", "source2"),
            row("c", "электромобиль и автомобили", "source1"),
            row("d", "электромобиль Tesla", "source1"),
            row("e", "про космос", "source3"),
        ]
        results = select_rankings(records, [.9, .8, .7, .6, .4],
                                  query="электромобиль", mode="text", limit=5,
                                  neural_weight=0, per_source=1)
        self.assertEqual(results[0]["post_id"], "c")
        self.assertEqual(len([r for r in results if r["source_key"] == "source1"]), 1)
        self.assertEqual(len([r for r in results if r["text_preview"] == "новости транспорта"]), 1)

    def test_visual_keeps_caption_free_documents(self):
        records = [row("a", "", "one"), row("b", "Пост без текста", "two")]
        result = select_rankings(records, [.5, .9], query="кот",
                                 mode="visual", limit=2)
        self.assertEqual([r["post_id"] for r in result], ["b", "a"])

    def test_invalid_input_prevented(self):
        records = [row("a", "мир"), row("a", "кот")]
        with self.assertRaisesRegex(ValueError, "Duplicate"):
            select_rankings(records, [.5, .7], query="q", mode="text", limit=3)
        with self.assertRaisesRegex(ValueError, "equal"):
            select_rankings(records, [1.], query="q", mode="text", limit=3)
        with self.assertRaisesRegex(ValueError, "Invalid"):
            select_rankings([row("a", "мир")], [.5], query="q",
                            mode="text", limit=1, neural_weight=1.2)

    def test_link_and_placeholder_filtering(self):
        self.assertFalse(useful_text("пост без текста"))
        self.assertTrue(useful_text("Серый кот на подоконнике"))
        self.assertEqual(post_link(row("a", "cat")), "https://example.com/a")


if __name__ == "__main__":
    unittest.main()
