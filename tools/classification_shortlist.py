#!/usr/bin/env python3
"""Candidate retrieval using taxonomy metadata and a local embedding service only.
Ranking never reads expected labels. Evaluation is a separate command.
"""
import argparse
import ctypes
import ctypes.util
import hashlib
import json
import math
import re
import urllib.request
from collections import Counter
from pathlib import Path

METHOD = {'version': 3, 'rrf_k': 60, 'weights': {'topic_guess': .55, 'description': .25, 'lexical': .20},
          'max_exact_alias_slots': 2, 'lexical_fields': {'title': 4, 'aliases': 3, 'synonyms': 3, 'about': 1, 'queries': .5},
          'negative_boundary_hints_indexed': False, 'cloud_calls': 0}
STOP = set('и в во на по о об из от до для при как что это то же а но или не нет да к с со у за без над под про его ее их мы вы я он она они нам нас вам вас свой наш ваш этот эта эти который которая которые когда где чем чтобы если уже только тоже все тут там быть есть был было будет the a an of to in on and or is are for with from this that it as by at be its публикация материал описание тема содержание'.split())


def read(path):
    return json.loads(Path(path).read_text())


def digest(data):
    return hashlib.sha256(json.dumps(data, ensure_ascii=False, sort_keys=True).encode()).hexdigest()


def write(path, data):
    p = Path(path)
    p.write_text(json.dumps(data, ensure_ascii=False, indent=2) + '\n')
    p.chmod(0o600)


def normalize_description(answer):
    """Legacy refusal is ambiguous: never silently promote it to a positive."""
    if not isinstance(answer, dict):
        raise ValueError('Description must be an object')
    description, guess = answer.get('description'), answer.get('topic_guess')
    if not isinstance(description, str) or not isinstance(guess, str):
        raise ValueError('Description and topic_guess must be strings')
    if 'topic_identifiable' in answer:
        if type(answer['topic_identifiable']) is not bool or type(answer.get('details_complete')) is not bool:
            raise ValueError('Separate topic/details flags must be boolean')
        identifiable, complete, legacy = answer['topic_identifiable'], answer['details_complete'], False
    else:
        if type(answer.get('sufficient')) is not bool:
            raise ValueError('No valid topic-identifiability flag')
        identifiable, complete, legacy = (True if answer['sufficient'] else None), None, True
    available = bool(description.strip() and guess.strip())
    return {'description': description.strip(), 'topic_guess': guess.strip(),
            'topic_identifiable': identifiable, 'details_complete': complete,
            'legacy_ambiguous_refusal': legacy and answer['sufficient'] is False,
            'legacy_sufficient': answer.get('sufficient') if legacy else None,
            'retrieval_text_available': available,
            'ready_for_label_selection': identifiable is True and available,
            'missing_details': answer.get('missing_details', answer.get('missing_content'))}


class Stemmer:
    def __init__(self):
        name = ctypes.util.find_library('stemmer')
        if not name:
            raise ValueError('Local libstemmer required; no downloads performed')
        self.lib = ctypes.CDLL(name)
        self.lib.sb_stemmer_new.argtypes = [ctypes.c_char_p, ctypes.c_char_p]
        self.lib.sb_stemmer_new.restype = ctypes.c_void_p
        self.lib.sb_stemmer_stem.argtypes = [ctypes.c_void_p, ctypes.c_char_p, ctypes.c_int]
        self.lib.sb_stemmer_stem.restype = ctypes.c_void_p
        self.lib.sb_stemmer_length.argtypes = [ctypes.c_void_p]
        self.lib.sb_stemmer_length.restype = ctypes.c_int
        self.lib.sb_stemmer_delete.argtypes = [ctypes.c_void_p]
        self.handles = {lang: self.lib.sb_stemmer_new(lang.encode(), b'UTF_8') for lang in ('russian', 'english')}
        if not all(self.handles.values()):
            raise ValueError('Local stemming languages unavailable')
        self.cache = {}

    def tokens(self, text, keep_stop=False):
        result = []
        for word in re.findall(r'[a-zа-яё0-9]+', text.lower().replace('ё', 'е')):
            if len(word) < 2 or (not keep_stop and word in STOP):
                continue
            if word not in self.cache:
                if len(word) <= 3:
                    self.cache[word] = word
                else:
                    raw = word.encode()
                    handle = self.handles['russian' if re.search('[а-я]', word) else 'english']
                    ptr = self.lib.sb_stemmer_stem(handle, raw, len(raw))
                    self.cache[word] = ctypes.string_at(ptr, self.lib.sb_stemmer_length(handle)).decode() or word
            result.append(self.cache[word])
        return result

    def close(self):
        for handle in self.handles.values():
            self.lib.sb_stemmer_delete(handle)
        self.handles = {}


def field_text(value):
    return ' '.join(value) if isinstance(value, list) else (value or '')


def embedding_document(topic):
    return '\n'.join(label + ': ' + field_text(topic.get(key)) for key, label in
                     [('title', 'Название'), ('about', 'Содержание'), ('aliases', 'Синонимы'),
                      ('synonyms', 'Близкие названия'), ('queries', 'Поисковые формулировки')]
                     if topic.get(key))


def unit(vector):
    length = math.sqrt(sum(x * x for x in vector))
    if not length:
        raise ValueError('Zero embedding')
    return [x / length for x in vector]


def local_embeddings(inputs, cache):
    fingerprint = digest(inputs)
    cache = Path(cache)
    if cache.exists():
        data = read(cache)
        if data['input_sha256'] != fingerprint:
            raise ValueError('Embedding cache belongs to other inputs; choose a new cache path')
        return data['vectors'], 0
    payload = {'model': 'bge-m3:latest', 'input': inputs, 'truncate': False, 'keep_alive': '1m'}
    write(cache.with_suffix('.attempt.json'), {'local_calls': 1, 'paid_calls': 0,
                                             'input_sha256': fingerprint, 'input_count': len(inputs)})
    request = urllib.request.Request('http://127.0.0.1:11434/api/embed', data=json.dumps(payload).encode(),
                                     headers={'Content-Type': 'application/json'}, method='POST')
    with urllib.request.urlopen(request, timeout=180) as response:
        data = json.loads(response.read())
    vectors = data['embeddings']
    if len(vectors) != len(inputs) or len({len(v) for v in vectors}) != 1:
        raise ValueError('Unaligned local embedding batch')
    write(cache, {'input_sha256': fingerprint, 'vectors': vectors})
    return vectors, 1


class CandidateIndex:
    def __init__(self, topics, vectors):
        if len(topics) != len(vectors) or len({t['key'] for t in topics}) != len(topics):
            raise ValueError('Invalid topic index')
        self.topics, self.vectors, self.stem = topics, [unit(v) for v in vectors], Stemmer()
        self.docs, self.aliases = [], {}
        for i, topic in enumerate(topics):
            counts = Counter()
            for field, weight in METHOD['lexical_fields'].items():
                for word in self.stem.tokens(field_text(topic.get(field))):
                    counts[word] += weight
            self.docs.append(counts)
            for phrase in [topic['title']] + (topic.get('aliases') or []) + (topic.get('synonyms') or []):
                tokens = tuple(self.stem.tokens(phrase, True))
                if not tokens or (len(tokens) == 1 and len(tokens[0]) < 4 and phrase.lower() not in ('ии', 'ai', 'hr')):
                    continue
                self.aliases.setdefault(tokens, set()).add(i)
        n = len(topics)
        df = Counter(word for doc in self.docs for word in doc)
        self.idf = {word: math.log(1 + (n - freq + .5) / (freq + .5)) for word, freq in df.items()}
        self.average = sum(sum(doc.values()) for doc in self.docs) / max(1, n)

    def rank(self, description, guess, description_vector, guess_vector):
        dvec, gvec = unit(description_vector), unit(guess_vector)
        semantic_guess = [sum(x * y for x, y in zip(gvec, v)) for v in self.vectors]
        semantic_description = [sum(x * y for x, y in zip(dvec, v)) for v in self.vectors]
        qweights = Counter()
        for text, weight in [(guess, .75), (description, .25)]:
            tokens = self.stem.tokens(text)
            for word, count in Counter(tokens).items():
                qweights[word] += weight * count / max(1, len(tokens))
        lexical = []
        for doc in self.docs:
            length = sum(doc.values())
            lexical.append(sum(weight * self.idf.get(word, 0) * doc[word] * 2.2 /
                               (doc[word] + 1.2 * (.35 + .65 * length / max(1, self.average)))
                               for word, weight in qweights.items() if word in doc))
        ranks = []
        for scores in [semantic_guess, semantic_description, lexical]:
            order = sorted(range(len(self.topics)), key=lambda i: (-scores[i], self.topics[i]['key']))
            ranks.append({i: pos + 1 for pos, i in enumerate(order)})
        weights = list(METHOD['weights'].values())
        fused = {i: sum(w / (METHOD['rrf_k'] + r[i]) for w, r in zip(weights, ranks)) for i in range(len(self.topics))}
        tokens = tuple(self.stem.tokens(guess, True))
        anchors = {}
        for phrase, ids in self.aliases.items():
            if len(ids) != 1:
                continue
            for start in range(len(tokens) - len(phrase) + 1):
                if tokens[start:start + len(phrase)] == phrase:
                    if set(tokens[max(0, start - 3):start]) & {'не', 'без', 'вместо'}:
                        continue
                    i = next(iter(ids))
                    strength = sum(self.idf.get(w, 0) for w in phrase)
                    if i not in anchors or strength > anchors[i]['strength']:
                        anchors[i] = {'phrase': ' '.join(phrase), 'strength': strength}
        reserved = sorted(anchors, key=lambda i: (-anchors[i]['strength'], -fused[i], self.topics[i]['key']))[:2]
        order = reserved + [i for i in sorted(fused, key=lambda i: (-fused[i], self.topics[i]['key'])) if i not in reserved]
        return [{'key': self.topics[i]['key'], 'title': self.topics[i]['title'], 'score': fused[i],
                 'exact_alias': anchors.get(i, {}).get('phrase'),
                 'ranks': {'topic_guess': ranks[0][i], 'description': ranks[1][i], 'lexical': ranks[2][i]}}
                for i in order[:5]]


def rank_saved(descriptions_path, taxonomy_path, cache, output):
    raw = read(descriptions_path)['descriptions']
    records = {key: normalize_description(answer) for key, answer in raw.items()}
    usable = {key: r for key, r in records.items() if r['retrieval_text_available']}
    topics = read(taxonomy_path)['leaves']
    inputs = [embedding_document(t) for t in topics]
    keys = sorted(usable)
    for key in keys:
        inputs.extend([usable[key]['description'], usable[key]['topic_guess']])
    # The fixed algorithm and input hash are recorded before any evaluation.
    write(Path(output).with_suffix('.method.json'), {'method': METHOD, 'input_sha256': digest(inputs),
                                                   'reference_labels_read': False})
    vectors, calls = local_embeddings(inputs, cache)
    index = CandidateIndex(topics, vectors[:len(topics)])
    try:
        for i, key in enumerate(keys):
            offset = len(topics) + 2 * i
            records[key]['candidates'] = index.rank(usable[key]['description'], usable[key]['topic_guess'],
                                                    vectors[offset], vectors[offset + 1])
    finally:
        index.stem.close()
    result = {'method': METHOD, 'records': records, 'local_embedding_calls': calls, 'new_paid_calls': 0,
              'taxonomy_sha256': digest(read(taxonomy_path)), 'description_source_sha256': digest(read(descriptions_path)),
              'labels_changed': False, 'note': 'Legacy ambiguous refusals ranked diagnostically, never converted to permission to label.'}
    write(output, result)
    return result


def evaluate(ranked, reference, baseline):
    records = ranked['records']
    old = baseline.get('retrieval_coverage', {}) if baseline else {}
    required = {key: set(ref['required']) for key, ref in reference['cases'].items()
                if ref['status'] != 'disputed' and ref['required']}
    rows = []
    for key, expected in required.items():
        row = records.get(key)
        candidates = [c['key'] for c in row.get('candidates', [])] if row else []
        rows.append({'id': key, 'required': sorted(expected), 'available': bool(candidates),
                     'any_at_3': bool(expected & set(candidates[:3])), 'all_at_3': expected <= set(candidates[:3]),
                     'any_at_5': bool(expected & set(candidates)), 'all_at_5': expected <= set(candidates),
                     'missing_at_5': sorted(expected - set(candidates)), 'candidates': candidates,
                     'legacy_ambiguous_refusal': row.get('legacy_ambiguous_refusal', False) if row else False})
    common = [r for r in rows if r['id'] in old]
    return {'reference_cases': len(rows), 'descriptions_available': sum(r['available'] for r in rows),
            'any_at_3': sum(r['any_at_3'] for r in rows), 'all_at_3': sum(r['all_at_3'] for r in rows),
            'any_at_5': sum(r['any_at_5'] for r in rows), 'all_at_5': sum(r['all_at_5'] for r in rows),
            'same_previous_subset': {'cases': len(common), 'before_all_at_5': sum(old[r['id']]['all_expected_present'] for r in common),
                                     'after_all_at_5': sum(r['all_at_5'] for r in common)},
            'rows': rows, 'new_paid_calls': 0, 'labels_changed': False,
            'limits': ['Retrieval coverage only; no new topic decision by a model', 'References remain provisional',
                       'Same small development set, not held-out accuracy', 'Legacy refusals remain pending']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    p = sub.add_parser('rank')
    for name in ['descriptions', 'taxonomy', 'cache', 'output']:
        p.add_argument('--' + name, type=Path, required=True)
    p = sub.add_parser('evaluate')
    for name in ['ranked', 'reference', 'output']:
        p.add_argument('--' + name, type=Path, required=True)
    p.add_argument('--baseline', type=Path)
    args = parser.parse_args()
    if args.command == 'rank':
        r = rank_saved(args.descriptions, args.taxonomy, args.cache, args.output)
        print(json.dumps({'records': len(r['records']), 'local_embedding_calls': r['local_embedding_calls'], 'new_paid_calls': 0}))
    else:
        r = evaluate(read(args.ranked), read(args.reference), read(args.baseline) if args.baseline else None)
        write(args.output, r)
        print(json.dumps({k: v for k, v in r.items() if k not in ('rows', 'limits')}))


if __name__ == '__main__':
    main()
