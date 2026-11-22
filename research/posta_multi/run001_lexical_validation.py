"""Evaluate word and character-trigram BM25 on disjoint validation queries.

The fixed calibration prefix is not an independent test. A separate suffix
tests whether its chosen reciprocal-rank fusion weight generalizes locally.
Both groups use the same candidate pool; only aggregate results are saved.
"""
import argparse
from collections import Counter, defaultdict
import json
import math
from pathlib import Path

from bm25_baseline import bm25_scores, words
from compare_checkpoints import atomic_report
from corpus import Corpus
from hybrid_retrieval import compare_splits, evaluate_rows
from retrieval_data_quality import curate_retrieval_pairs, normalized


def character_trigrams(text):
    result = []
    for word in words(text):
        padded = '^' + word + '$'
        result.extend(padded[i:i+3] for i in range(max(0, len(padded)-2)))
    return result


class CharacterBM25:
    def __init__(self, documents, k1=1.2, b=0.75):
        if not documents or not 0 < k1 < 5 or not 0 <= b <= 1:
            raise ValueError('invalid BM25 corpus or parameters')
        self.k1, self.b, self.n = k1, b, len(documents)
        self.lengths, self.postings = [], defaultdict(list)
        for index, text in enumerate(documents):
            counts = Counter(character_trigrams(text))
            self.lengths.append(sum(counts.values()))
            for term, count in counts.items():
                self.postings[term].append((index, count))
        self.avg = sum(self.lengths) / self.n

    def scores(self, query):
        result = [0.0] * self.n
        if not self.avg:
            return result
        for term, qfrequency in Counter(character_trigrams(query)).items():
            entries = self.postings.get(term, ())
            if not entries:
                continue
            idf = math.log1p((self.n - len(entries) + .5) / (len(entries) + .5))
            for index, frequency in entries:
                denominator = frequency + self.k1 * (
                    1-self.b+self.b*self.lengths[index]/self.avg)
                result[index] += qfrequency * idf * frequency*(self.k1+1)/denominator
        return result


def evaluate(corpus, samples=128, calibration=64):
    pairs, pairs_sha, _ = curate_retrieval_pairs(corpus, samples, split='validation', avoid_heldout=False)
    prefix, prefix_sha, _ = curate_retrieval_pairs(corpus, calibration, split='validation', avoid_heldout=False)
    if pairs[:calibration] != prefix or samples <= calibration:
        raise ValueError('invalid validation split')
    docs = list(dict.fromkeys(value for _, pos, neg in pairs for value in (pos, neg)))
    doc_index = {doc: i for i, doc in enumerate(docs)}
    indexed = [words(doc) for doc in docs]
    char_index = CharacterBM25(docs)
    rows = [{'lexical': bm25_scores(q, indexed), 'semantic': char_index.scores(q),
             'positive': doc_index[p], 'negative': doc_index[n]} for q, p, n in pairs]
    result = compare_splits(rows, calibration)
    # These names are lexical methods, not neural embeddings.
    result['evaluation']['char_bm25'] = result['evaluation'].pop('semantic')
    result['selected_char_weight'] = result.pop('selected_semantic_weight')
    languages = defaultdict(set)
    selected = {normalized(q) for q, _, _ in pairs[calibration:]}
    for row in corpus.rows('retrieval', 'validation'):
        key = normalized(row.get('text'))
        if key in selected:
            language = row.get('language')
            languages[key].add(language if isinstance(language, str) and language else 'und')
    by_language = defaultdict(list)
    for (query, _, _), row in zip(pairs[calibration:], rows[calibration:]):
        labels = languages.get(normalized(query), set())
        label = next(iter(labels)) if len(labels) == 1 else 'mixed_or_unknown'
        by_language[label].append(row)
    result['evaluation_by_language'] = {
        label: {'word_bm25': evaluate_rows(group, 0.0),
                'char_bm25': evaluate_rows(group, 1.0),
                'selected_hybrid': evaluate_rows(group, result['selected_char_weight'])}
        for label, group in sorted(by_language.items())}
    return {'format': 'posta-run001-lexical-internal-validation-v1',
            'manifest_sha256': corpus.manifest_hash, 'validation_pairs_sha256': pairs_sha,
            'calibration_pairs_sha256': prefix_sha, 'candidate_pool_size': len(docs),
            'methods': ['word BM25', 'character-trigram BM25', 'reciprocal-rank fusion'],
            'rank_constant': 60, **result,
            'limitations': ['internal validation, not independent test data',
                            'candidate pool includes documents from both query groups',
                            'unjudged candidates may be relevant',
                            'sample does not establish general topic or language coverage']}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.output.exists():
        raise FileExistsError('lexical report already exists')
    report = evaluate(Corpus(args.dataset))
    atomic_report(args.output, report)
    print(json.dumps(report, ensure_ascii=False), flush=True)


if __name__ == '__main__':
    main()
