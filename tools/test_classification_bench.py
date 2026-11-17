import importlib.util
import json
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SPEC = importlib.util.spec_from_file_location('bench', Path(__file__).with_name('classification_bench.py'))
B = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(B)
TREE = {'parents': {'business': 'Бизнес'}, 'leaves': [
    {'key': 'business.logistics', 'title': 'Логистика', 'about': 'Таможня и поставки'},
    {'key': 'finance.taxes', 'title': 'Налоги', 'about': 'Налоги'},
    {'key': 'tech.code', 'title': 'Код', 'about': 'Написание кода'}]}
REFS = {'cases': {'1': {'required': ['business.logistics'], 'allowed': [],
                        'forbidden': ['tech.code'], 'status': 'provisional',
                        'disputed_topics': {'finance.taxes': 'Editorial policy pending'}}}}


def prediction(topics, sufficient=True):
    return {'answer': {'sufficient': sufficient, 'topics': topics}}


class ScoringTests(unittest.TestCase):
    def test_wrong_extra_missing_and_refusal_are_distinct(self):
        wrong = B.score_one('1', prediction(['tech.code']), REFS, TREE)
        self.assertEqual(wrong['wrong_topics'], ['tech.code'])
        self.assertEqual(wrong['missing_topics'], ['business.logistics'])
        extra_tree = dict(TREE, leaves=TREE['leaves'] + [{'key': 'food.bread', 'title': 'Хлеб', 'about': ''}])
        extra = B.score_one('1', prediction(['business.logistics', 'food.bread']), REFS, extra_tree)
        self.assertEqual(extra['extra_topics'], ['food.bread'])
        self.assertFalse(extra['wrong_topics'])
        refusal = B.score_one('1', prediction([], False), REFS, TREE)
        self.assertEqual(refusal['outcome'], 'abstained')
        self.assertFalse(refusal['wrong_topics'] or refusal['missing_topics'])
        empty = B.score_one('1', prediction([]), REFS, TREE)
        self.assertEqual(empty['outcome'], 'topic_errors')
        self.assertEqual(empty['missing_topics'], ['business.logistics'])

    def test_disputed_tax_is_neither_extra_nor_strict_match(self):
        row = B.score_one('1', prediction(['Логистика', 'Налоги']), REFS, TREE)
        self.assertEqual(row['outcome'], 'reference_review_needed')
        self.assertEqual(row['disputed_topics'], ['finance.taxes'])
        self.assertFalse(row['extra_topics'] or row['eligible_for_strict_accuracy'])

    def test_broad_parent_is_separate_from_wrong_topic(self):
        row = B.score_one('1', prediction(['Бизнес']), REFS, TREE)
        self.assertEqual(row['too_general'], ['business'])
        self.assertFalse(row['wrong_topics'])
        self.assertEqual(row['missing_topics'], ['business.logistics'])

    def test_outage_and_incomplete_input_do_not_reduce_strict_accuracy(self):
        outage = B.score_one('1', {'error': 'HTTP 429'}, REFS, TREE)
        incomplete = B.score_one('1', prediction(['tech.code']), REFS, TREE, False)
        good = B.score_one('1', prediction(['business.logistics']), REFS, TREE)
        summary = B.summarize([outage, incomplete, good])
        self.assertEqual(summary['strict_denominator'], 1)
        self.assertEqual(summary['strict_matches'], 1)
        self.assertEqual(summary['transport_errors'], 1)
        self.assertFalse(outage['abstained'])

    def test_conflicting_and_unknown_answers_rejected(self):
        self.assertEqual(B.score_one('1', prediction(['business.logistics'], False), REFS, TREE)['outcome'], 'invalid_response')
        self.assertEqual(B.score_one('1', prediction(['invented']), REFS, TREE)['outcome'], 'invalid_labels')
        self.assertEqual(B.score_one('1', {'answer': {}}, REFS, TREE)['outcome'], 'invalid_response')

    def test_unrelated_parent_is_wrong_not_merely_general(self):
        tree = dict(TREE, parents=dict(TREE['parents'], animals='Животные'))
        row = B.score_one('1', prediction(['Животные']), REFS, tree)
        self.assertEqual(row['wrong_topics'], ['animals'])
        self.assertFalse(row['too_general'])

    def test_reference_revision_preserves_disputes(self):
        refs = B.read(B.DEFAULT_REFERENCE)
        rubber = refs['cases']['17900']
        self.assertEqual(rubber['status'], 'disputed')
        self.assertNotIn('business.owners', rubber['forbidden'])
        self.assertIn('finance.taxes', refs['cases']['17902']['disputed_topics'])


class PreparationTests(unittest.TestCase):
    def test_full_text_and_additional_article_never_sliced(self):
        text = 'Пост ' + 'а' * 5000
        article = 'Статья ' + 'б' * 6000
        combined = B.combined_text({'text': text, 'article_text': article})
        self.assertIn(text, combined)
        self.assertIn(article, combined)
        self.assertEqual(B.combined_text({'text': text + article, 'article_text': article}), text + article)

    def test_prepare_and_build_do_not_leak_reference_labels(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            post = 'Таможенное оформление ' + 'данные ' * 1000
            B.write(root / 'old.json', [{'id': '1', 'kind': 'text'}])
            B.write(root / 'tree.json', TREE)
            B.write(root / 'ref.json', REFS)
            (root / 'snapshot.jsonl').write_text(json.dumps({'store': 'pool', 'key': '1', 'data': {'text': post}, 'tags': []}) + '\n')
            manifest = B.prepare(root / 'snapshot.jsonl', root / 'old.json', root / 'out', root / 'ref.json', root / 'tree.json')
            request = B.build_request(manifest, TREE, '1', 'test', ['text'])
            prompt = request['payload']['input'][0]['content'][0]['text']
            self.assertIn(post, prompt)
            self.assertIn('topic_identifiable', prompt)
            self.assertIn('details_complete', prompt)
            self.assertNotIn('{id,sufficient', prompt)
            self.assertNotIn('finance.taxes', prompt)
            self.assertFalse(request['sent'])
            with self.assertRaisesRegex(ValueError, '3–5'):
                B.build_request(manifest, TREE, '1', 'test', ['text'], 'classify', ['business'])
            with self.assertRaisesRegex(ValueError, 'Frozen benchmark'):
                B.prepare(root / 'snapshot.jsonl', root / 'old.json', root / 'out', root / 'ref.json', root / 'tree.json')

    def test_changed_text_cannot_reuse_old_input_fingerprint(self):
        case = {'id': '1', 'kind': 'text', 'full_text': 'original', 'ready': True,
                'issues': [], 'media': {}}
        case['input_fingerprint'] = B.digest({'text': case['full_text'], 'kind': 'text', 'media': {}})
        case['full_text'] = 'silently replaced'
        with self.assertRaisesRegex(ValueError, 'Frozen input changed'):
            B.build_request({'cases': [case], 'reference_sha256': 'r'}, TREE, '1', 'test', ['text'])

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'ffmpeg required')
    def test_full_video_and_sound_required_not_title_fallback(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            original = root / 'original.mp4'
            subprocess.run(['ffmpeg', '-v', 'error', '-f', 'lavfi', '-i', 'color=c=blue:s=64x64:r=10',
                            '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=16000', '-t', '1',
                            '-c:v', 'mpeg4', '-c:a', 'aac', '-y', str(original)], check=True, capture_output=True)
            out = root / 'frozen'
            out.mkdir()
            media = B.prepare_video(original, out)
            self.assertTrue(media['has_audio'])
            self.assertGreater(media['audio']['duration_seconds'], .8)
            self.assertEqual(media['video']['sha256'], B.file_digest(original))
            case = {'id': '1', 'kind': 'video', 'full_text': 'Заголовок', 'ready': True,
                    'issues': [], 'media': media, 'input_fingerprint': B.digest({'text': 'Заголовок', 'kind': 'video', 'media': media})}
            manifest = {'cases': [case], 'reference_sha256': 'reference'}
            with self.assertRaisesRegex(ValueError, 'Speech unavailable'):
                B.build_request(manifest, TREE, '1', 'video-only', ['text', 'image', 'video'])
            req = B.build_request(manifest, TREE, '1', 'audio-video', ['text', 'image', 'video', 'audio'])
            kinds = [part['type'] for part in req['payload']['input'][0]['content']]
            self.assertEqual(kinds, ['input_text', 'input_video', 'input_audio'])
            Path(media['video']['path']).write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'Frozen media changed'):
                B.build_request(manifest, TREE, '1', 'audio-video', ['text', 'video', 'audio'])


if __name__ == '__main__':
    unittest.main()
