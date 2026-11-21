"""Streaming, hash-verified access to the local Posta dataset candidate.

The manifest's unresolved review flags are preserved and reported to callers;
access is not evidence that upstream rights or quality have been cleared.
"""
import hashlib
import json
from pathlib import Path

KINDS = ('text', 'retrieval', 'image_text', 'ocr_text', 'meme_text', 'video_text', 'speech_text')


def sha256(stream):
    return hashlib.file_digest(stream, 'sha256').hexdigest()


class Corpus:
    def __init__(self, root):
        self.root = Path(root).resolve(strict=True)
        self.manifest_path = self.root / 'manifest.json'
        raw = self.manifest_path.read_bytes()
        self.manifest_hash = hashlib.sha256(raw).hexdigest()
        self.manifest = json.loads(raw)

    def rows(self, kind, split='train'):
        if kind not in KINDS or split not in ('train', 'validation', 'test'):
            raise ValueError('invalid kind or split')
        name = f'{kind}-{split}.jsonl'
        matches = [x for x in self.manifest['files'] if x.get('path') == name]
        if len(matches) != 1:
            raise ValueError(f'missing or ambiguous manifest entry: {name}')
        path = self.root / name
        if path.is_symlink() or not path.is_file():
            raise ValueError('invalid corpus file')
        with path.open('rb') as f:
            if sha256(f) != matches[0]['sha256']:
                raise ValueError(f'hash mismatch: {name}')
            f.seek(0)
            for line in f:
                row = json.loads(line)
                if row.get('kind') != kind or row.get('split') != split:
                    raise ValueError('unexpected kind or split')
                if not all(isinstance(row.get(k), str) and row[k] for k in ('dataset_id', 'dataset_revision', 'text')):
                    raise ValueError('missing text or provenance')
                yield row

    def media_path(self, row):
        media = row.get('media')
        if not isinstance(media, dict):
            raise ValueError('missing media')
        relative = Path(media.get('path', ''))
        if relative.is_absolute() or '..' in relative.parts:
            raise ValueError('invalid media path')
        path = (self.root / relative).resolve(strict=True)
        if not path.is_relative_to(self.root / 'media') or not path.is_file():
            raise ValueError('media outside corpus media directory')
        with path.open('rb') as f:
            if sha256(f) != media.get('sha256'):
                raise ValueError('media hash mismatch')
        return path
