"""Read-only CPU diagnostic of image and temporal embedding geometry.

Uses one frame from each held-out video. This isolates stages; it is not a
video-understanding benchmark and it does not write or modify weights.
"""
import argparse
import json

import numpy as np
import torch
import torch.nn.functional as F

from av_data import select_media_pairs
from av_segments import decode_window, probe
from av_temporal import aggregate_video_windows
from corpus import Corpus
from model import PostaMulti
from spec import Config, parameter_counts


def geometry(vectors):
    vectors = F.normalize(torch.stack(vectors).float(), dim=-1)
    mask = ~torch.eye(vectors.shape[0], dtype=torch.bool)
    similarities = (vectors @ vectors.T)[mask]
    return {'mean_pairwise_cosine': float(similarities.mean()),
            'min_pairwise_cosine': float(similarities.min()),
            'max_pairwise_cosine': float(similarities.max())}


def run(dataset, checkpoint, limit=4):
    if not 2 <= limit <= 8:
        raise ValueError('diagnostic requires 2-8 held-out videos')
    torch.set_num_threads(2)
    corpus = Corpus(dataset)
    state = torch.load(checkpoint, map_location='cpu', weights_only=True, mmap=True)
    settings = state.get('settings', {})
    if (state.get('format') != 'posta-multi-235m-run001-text-v3-best' or
            settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('quarantined_research_only') is not True or
            settings.get('production_promotion_allowed') is not False):
        raise ValueError('source checkpoint provenance mismatch')
    config = Config(**settings['config'])
    if parameter_counts(config)['total'] != 235179777:
        raise ValueError('unexpected model size')
    model = PostaMulti(config, dropout=0).eval()
    model.load_state_dict(state['model'])
    del state
    rows, selection = select_media_pairs(corpus, 'video_text', 'validation', limit)
    images, temporal, timed, centered, multi_legacy, multi_centered = [], [], [], [], [], []
    residual_half, residual_one, residual_two = [], [], []
    window_mean, window_hierarchical, window_residual, reverse_cosines = [], [], [], []
    with torch.inference_mode():
        for row in rows:
            part = decode_window(corpus.media_path(row), 0, 1, fps=1,
                                 image_size=config.image_size, has_audio=False)
            frame = torch.from_numpy(np.array(part['frames'][0])).permute(2, 0, 1).unsqueeze(0).float()/255
            image = model.encode_image(frame)
            images.append(image.squeeze(0))
            temporal.append(model.encode_video_embeddings(image.unsqueeze(1)).squeeze(0))
            timed.append(model.encode_video_embeddings(image.unsqueeze(1),
                         timestamps=torch.tensor([[0.]])).squeeze(0))
            centered.append(model.encode_video_embeddings(image.unsqueeze(1),
                            timestamps=torch.tensor([[0.]]),
                            time_encoding='relative-centered-v1').squeeze(0))
            duration = min(2., probe(corpus.media_path(row))['duration_seconds'])
            part = decode_window(corpus.media_path(row), 0, duration, fps=2,
                                 image_size=config.image_size, has_audio=False)
            frames = torch.from_numpy(np.array(part['frames'])).permute(0, 3, 1, 2).float()/255
            embeddings = model.encode_image(frames).unsqueeze(0)
            times = torch.tensor([part['frame_timestamps']])
            multi_legacy.append(model.encode_video_embeddings(embeddings, timestamps=times).squeeze(0))
            encoded = model.encode_video_embeddings(embeddings, timestamps=times,
                                                    time_encoding='relative-centered-v1').squeeze(0)
            multi_centered.append(encoded)
            mean_frame = F.normalize(embeddings.float().mean(dim=1), dim=-1).squeeze(0)
            residual_half.append(F.normalize(encoded.float() + .5 * mean_frame, dim=-1))
            residual_one.append(F.normalize(encoded.float() + mean_frame, dim=-1))
            residual_two.append(F.normalize(encoded.float() + 2 * mean_frame, dim=-1))
            duration = min(4., probe(corpus.media_path(row))['duration_seconds'])
            part = decode_window(corpus.media_path(row), 0, duration, fps=2,
                                 image_size=config.image_size, has_audio=False)
            frames = torch.from_numpy(np.array(part['frames'])).permute(0, 3, 1, 2).float()/255
            frame_vectors = model.encode_image(frames)
            times = torch.tensor(part['frame_timestamps'])
            windows, starts = [], []
            for offset in range(0, len(frame_vectors), 4):
                chunks = frame_vectors[offset:offset+4].unsqueeze(0)
                chunk_times = times[offset:offset+4].unsqueeze(0)
                windows.append(model.encode_video_embeddings(chunks, timestamps=chunk_times,
                               time_encoding='relative-centered-v1').squeeze(0))
                starts.append(chunk_times[0, 0])
            window_vectors = torch.stack(windows).unsqueeze(0)
            window_times = torch.stack(starts).unsqueeze(0)
            mean = aggregate_video_windows(model, window_vectors, window_times, mode='mean')
            hierarchical = aggregate_video_windows(model, window_vectors, window_times,
                mode='hierarchical-v1', time_encoding='relative-centered-v1')
            residual = aggregate_video_windows(model, window_vectors, window_times,
                mode='hierarchical-residual-v1', time_encoding='relative-centered-v1')
            reversed_order = aggregate_video_windows(model, window_vectors.flip(1), window_times,
                mode='hierarchical-v1', time_encoding='relative-centered-v1')
            window_mean.append(mean.squeeze(0)); window_hierarchical.append(hierarchical.squeeze(0))
            window_residual.append(residual.squeeze(0))
            reverse_cosines.append(float(F.cosine_similarity(hierarchical, reversed_order).item()))
    return {'format': 'posta-av-collapse-diagnostic-v1', 'examples': limit,
            'selection_sha256': selection, 'one_frame_per_video': True,
            'image_encoder': geometry(images), 'temporal_without_time': geometry(temporal),
            'temporal_with_time': geometry(timed),
            'temporal_with_centered_time': geometry(centered),
            'two_second_2fps_legacy': geometry(multi_legacy),
            'two_second_2fps_centered': geometry(multi_centered),
            'exploratory_frame_residual_0_5': geometry(residual_half),
            'exploratory_frame_residual_1': geometry(residual_one),
            'exploratory_frame_residual_2': geometry(residual_two),
            'window_mean': geometry(window_mean),
            'window_hierarchical': geometry(window_hierarchical),
            'window_hierarchical_residual': geometry(window_residual),
            'window_hierarchy_mean_cosine_to_reversed_order': sum(reverse_cosines) / len(reverse_cosines),
            'limitation': 'four clips and up to four seconds each; geometry only, no semantic quality evaluation'}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--checkpoint', required=True)
    args = parser.parse_args()
    print(json.dumps(run(args.dataset, args.checkpoint)), flush=True)
