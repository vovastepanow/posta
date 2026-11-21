"""Fail-closed reader for explicitly cleared, hash-verified retrieval data."""
import hashlib
import json
from pathlib import Path


def verified_retrieval_rows(directory):
    root = Path(directory).resolve()
    manifest = json.loads((root / 'manifest.json').read_text(encoding='utf-8'))
    if manifest.get('status') != 'approved_for_training':
        raise ValueError('dataset manifest is not approved for training')
    entries = [entry for entry in manifest.get('files', []) if entry.get('path') == 'retrieval-train.jsonl']
    if len(entries) != 1:
        raise ValueError('manifest must identify one retrieval train file')
    entry = entries[0]
    path = root / 'retrieval-train.jsonl'
    if path.is_symlink() or not path.is_file():
        raise ValueError('retrieval file must be a regular file')
    with path.open('rb') as stream:
        digest = hashlib.file_digest(stream, 'sha256').hexdigest()
        if digest != entry.get('sha256'):
            raise ValueError('dataset file hash mismatch')
        stream.seek(0)
        for raw in stream:
            row = json.loads(raw)
            if row.get('training_approved') is not True or row.get('split') != 'train' or row.get('kind') != 'retrieval':
                raise ValueError('row is not an approved retrieval training example')
            if not all(isinstance(row.get(k), str) and row[k].strip() for k in ('text', 'positive', 'dataset_id', 'dataset_revision')):
                raise ValueError('row lacks retrieval text or source provenance')
            yield row


def tokenized_retrieval_rows(directory, max_length=512):
    """Yield individually tokenized positive pairs; negatives are not inferred."""
    if __package__:
        from .tokenizer import encode
    else:
        from tokenizer import encode
    for row in verified_retrieval_rows(directory):
        yield {'query_ids': encode(row['text'], max_length),
               'positive_ids': encode(row['positive'], max_length),
               'dataset_id': row['dataset_id'], 'dataset_revision': row['dataset_revision'],
               'record_id': row.get('record_id')}
