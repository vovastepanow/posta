import importlib.util
import json
import pathlib
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("review", pathlib.Path(__file__).resolve().parents[1] / "openrouter_review.py")
review = importlib.util.module_from_spec(spec)
spec.loader.exec_module(review)

class BlueskyFeedTest(unittest.TestCase):
    def test_handle_resolves_author_did_and_excludes_reposts(self):
        post = {"author": {"did": "did:plc:abc", "handle": "Example.bsky.social"},
                "uri": "at://did:plc:abc/app.bsky.feed.post/123", "record": {"text": "hello"}}
        body = json.dumps({"feed": [{"post": post}, {"post": post, "reason": {"type": "repost"}},
                                    {"post": {**post, "uri": "at://did:plc:other/app.bsky.feed.post/999"}}]})
        with patch.object(review, "request", return_value=body):
            status, posts = review.fetch_posts({"source_id": "rss:https://bsky.app/profile/example.bsky.social/rss"}, 30)
        self.assertEqual(status, "bluesky_public_author_feed")
        self.assertEqual([p["url"] for p in posts], ["https://bsky.app/profile/did:plc:abc/post/123"])

    def test_did_actor_does_not_accept_different_author(self):
        body = json.dumps({"feed": [{"post": {"author": {"did": "did:plc:other"}, "uri": "at://did:plc:other/app.bsky.feed.post/123"}}]})
        with patch.object(review, "request", return_value=body):
            _, posts = review.fetch_posts({"source_id": "rss:https://bsky.app/profile/did:plc:abc/rss"}, 30)
        self.assertEqual(posts, [])

if __name__ == "__main__": unittest.main()
