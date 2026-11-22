"""Read-only, single-second AV inference probe; never trains or saves weights.

Run only when no other GPU training/inference job is active. This measures
feasibility, not AV quality: Run 001 has not trained its video/audio towers.
"""
import argparse
import json
import time

import torch

from av_real_smoke import log_mel_audio, select_real_video
from av_segments import decode_window
from corpus import Corpus
from model import PostaMulti
from spec import Config, parameter_counts


def run(dataset, checkpoint):
    if not torch.cuda.is_available():
        raise RuntimeError('CUDA unavailable; no probe performed')
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    corpus = Corpus(dataset)
    path, metadata = select_real_video(corpus)
    state = torch.load(checkpoint, map_location='cpu', weights_only=True, mmap=True)
    settings = state.get('settings', {})
    if (state.get('format') != 'posta-multi-235m-run001-text-v3-best' or
            settings.get('manifest_sha256') != corpus.manifest_hash or
            settings.get('quarantined_research_only') is not True or
            settings.get('private_local_only') is not True or
            settings.get('production_promotion_allowed') is not False):
        raise ValueError('checkpoint provenance or quarantine policy mismatch')
    config = Config(**settings['config'])
    checkpoint_step = state.get('step')
    if parameter_counts(config)['total'] != 235179777:
        raise ValueError('unexpected architecture')
    segment = decode_window(path, 0, 1, fps=30, image_size=config.image_size,
                            has_audio=metadata['has_audio_stream'])
    model = PostaMulti(config, dropout=0).half().cuda().eval()
    model.load_state_dict(state['model'], strict=True)
    del state
    torch.cuda.reset_peak_memory_stats()
    started = time.monotonic()
    with torch.inference_mode():
        representations = []
        for frame in segment['frames']:
            image = torch.from_numpy(frame.copy()).permute(2, 0, 1).unsqueeze(0)
            image = image.to(device='cuda', dtype=torch.float16).div_(255)
            representations.append(model.encode_image(image))
        frames = torch.stack(representations, dim=1)
        times = torch.tensor([segment['frame_timestamps']], device='cuda')
        video = model.encode_video_embeddings(frames, timestamps=times)
        mel = log_mel_audio(segment['audio'], config.mel_bins).to(device='cuda', dtype=torch.float16)
        audio = model.encode_audio(mel)
        slots = torch.stack((torch.zeros_like(video), torch.zeros_like(video), video, audio), dim=1)
        fused = model.fuse_post(slots, torch.tensor([[False, False, True, True]], device='cuda'))
        if not all(torch.isfinite(x).all() for x in (video, audio, fused)):
            raise RuntimeError('nonfinite output')
    torch.cuda.synchronize()
    return {'format': 'posta-av-235m-feasibility-v1', 'checkpoint_step': checkpoint_step,
            'source_duration_seconds': metadata['duration_seconds'], 'source_fps': metadata['source_fps'],
            'target_fps': 30, 'window_seconds': 1, 'frames': len(segment['frames']),
            'audio_samples': len(segment['audio']), 'elapsed_seconds': round(time.monotonic()-started, 3),
            'cuda_peak_allocated_mib': round(torch.cuda.max_memory_allocated()/2**20, 1),
            'visual_audio_weights_trained': False, 'quality_verified': False,
            'limitations': ['single window, excludes model loading and media decoding from timing',
                            'not a realtime or end-to-end benchmark',
                            'video/audio/fusion weights were not trained in this text stage']}


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--dataset', required=True)
    parser.add_argument('--checkpoint', required=True)
    args = parser.parse_args()
    print(json.dumps(run(args.dataset, args.checkpoint)), flush=True)
