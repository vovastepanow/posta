import unittest

from source_autopilot_replay import summarize


class ReplayTest(unittest.TestCase):
    def test_unknown_labels_and_decisions_are_not_successes(self):
        samples = [
            {"profile": "A", "annotation": {"relevant": True, "duplicate": False}, "decisions": {"auto": True, "baseline": False}},
            {"profile": "A", "annotation": {"relevant": False, "duplicate": True}, "decisions": {"auto": True, "baseline": True}},
            {"profile": "A", "annotation": None, "decisions": {"auto": True}},
            {"profile": "A", "annotation": {"relevant": True}},
        ]
        result = summarize(samples)["A"]["systems"]
        self.assertEqual(result["auto"]["admitted"], 3)
        self.assertEqual(result["auto"]["relevance_n"], 2)
        self.assertEqual(result["auto"]["relevance_rate"], 0.5)
        self.assertEqual(result["auto"]["duplicate_rate"], 0.5)
        self.assertEqual(result["baseline"]["relevance_rate"], 0)
        self.assertIsNone(result["auto"]["advertising_rate"])


if __name__ == "__main__":
    unittest.main()
