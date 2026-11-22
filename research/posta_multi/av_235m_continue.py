"""Resumable REAL 235,179,777-parameter video+speech continuation on local data.

Starts from the completed private 1-step pilot, rotates isolated training
pairs, and checkpoints both weights and CPU-master AdamW optimizer. GPU is
bounded; no external downloads, no production writes, no automatic promotion.
This remains an experimental alignment stage, not a verified media model.
"""
import argparse
from dataclasses import asdict
import hashlib
import json
import shutil
import time
from pathlib import Path

import numpy as np
import torch
from tokenizers import Tokenizer

from av_alignment import alignment_backward, configure_av_trainable
from av_continuation_policy import (
    pair_indices, select_training_pools, validated_progress,
)
from av_provenance import validate_av_source
from av_real_smoke import log_mel_audio
from av_streaming import iter_segments_streaming
from corpus import Corpus
from corpus_smoke import ffmpeg
from cpu_master_adam import CPUMasterAdamW
from model import PostaMulti
from spec import Config, parameter_counts
from train_text import save_atomic

FORMAT = "posta-av-235m-continuation-v1"


def _sha(path):
    with Path(path).open("rb") as source:
        return hashlib.file_digest(source, "sha256").hexdigest()


def _validate_pilot(source, sha, manifest_hash, tokenizer_hash):
    if source.get("format") != "posta-av-235m-private-pilot-v1":
        raise ValueError("continuation requires the completed private AV pilot")
    settings = source.get("settings")
    if (type(settings) is not dict or
            settings.get("quarantined_research_only") is not True or
            settings.get("private_local_only") is not True or
            settings.get("production_promotion_allowed") is not False or
            settings.get("manifest_sha256") != manifest_hash or
            settings.get("tokenizer_sha256") != tokenizer_hash or
            settings.get("step") != 1 or
            settings.get("video_time_encoding") != "relative-centered-v1" or
            settings.get("video_window_aggregation") != "hierarchical-residual-v1" or
            type(settings.get("config")) is not dict or
            "model" not in source):
        raise ValueError("pilot provenance, encoding or quarantine mismatch")
    config = Config(**settings["config"])
    if parameter_counts(config)["total"] != 235179777:
        raise ValueError("unexpected 235M architecture")
    return settings, config


def _text_target(model, tokenizer, row, device):
    ids = tokenizer.encode(row["text"]).ids
    if len(ids) < 3 or ids[0] != 1 or ids[-1] != 2:
        raise ValueError("invalid caption/transcript tokenization")
    token_ids = torch.tensor([ids], dtype=torch.long, device=device)
    with torch.no_grad():
        return model.encode_text(
            token_ids, torch.ones_like(token_ids, dtype=torch.bool)
        ).squeeze(0).float()


def _video_training_pair(model, corpus, tokenizer, row, config, device):
    """Keep the decoded float pixels on CPU; image frames transfer singly."""
    target = _text_target(model, tokenizer, row, device)
    windows = []
    for segment in iter_segments_streaming(
            corpus.media_path(row), fps=30, window_seconds=2,
            image_size=config.image_size):
        frames = (torch.from_numpy(segment["frames"]).permute(0, 3, 1, 2)
                  .unsqueeze(0).float().div_(255))
        timestamps = torch.tensor([segment["frame_timestamps"]], dtype=torch.float32)
        windows.append((frames, timestamps))
    if not windows:
        raise ValueError("empty video training example")
    return target, windows


def _speech_training_pair(model, corpus, tokenizer, row, config, device):
    target = _text_target(model, tokenizer, row, device)
    raw = ffmpeg("-i", corpus.media_path(row), "-ac", "1", "-ar", "16000",
                 "-f", "f32le", "pipe:1")
    wave = np.frombuffer(raw, dtype="<f4").copy()
    if not 512 <= len(wave) <= 16_000 * 30 or not np.isfinite(wave).all():
        raise ValueError("invalid speech duration or samples")
    return target, log_mel_audio(wave, config.mel_bins)


def train(args):
    if (args.steps < 2 or args.steps > 256 or args.save_every < 1 or
            args.save_every > args.steps or
            not 0 < args.lr <= 1e-5):
        raise ValueError("invalid bounded AV continuation settings")
    if not torch.cuda.is_available():
        raise RuntimeError("this training stage needs local CUDA")
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    torch.manual_seed(235017)
    device = torch.device("cuda")
    corpus = Corpus(args.dataset)
    tokenizer_hash = _sha(args.tokenizer)
    source_hash = _sha(args.source)
    source = torch.load(args.source, weights_only=True, map_location="cpu", mmap=True)
    _, config = _validate_pilot(source, source_hash, corpus.manifest_hash, tokenizer_hash)
    tokenizer = Tokenizer.from_file(str(args.tokenizer))
    tokenizer.enable_truncation(max_length=128)
    for special, token in (("[PAD]", 0), ("[CLS]", 1), ("[SEP]", 2)):
        if tokenizer.token_to_id(special) != token:
            raise ValueError("unexpected tokenizer special IDs")

    # Validate that the source one-step pilot was measured before further work.
    # This deliberately refuses to use an unverified or mismatched checkpoint.
    comparison = json.loads(Path(args.comparison).read_text(encoding="utf-8"))
    if (comparison.get("format") != "posta-av-private-baseline-comparison-v1" or
            comparison.get("source_checkpoint_sha256") !=
            source["settings"].get("source_checkpoint_sha256") or
            comparison.get("tokenizer_sha256") != tokenizer_hash or
            comparison.get("manifest_sha256") != corpus.manifest_hash or
            comparison.get("production_promotion_allowed") is not False):
        raise ValueError("missing or mismatched completed AV pilot comparison")
    if (comparison.get("checkpoint_hashes_verified", {}).get("after") is not True):
        raise ValueError("pilot output checkpoint hash must be verified before continuation")
    # Re-check the comparison's after hash through train.json; do not accept
    # the summary alone if it is not bound to the actual checkpoint file.
    pilot_summary = json.loads(Path(args.pilot_summary).read_text(encoding="utf-8"))
    if (pilot_summary.get("output_checkpoint_sha256") != source_hash or
            pilot_summary.get("manifest_sha256") != corpus.manifest_hash or
            pilot_summary.get("tokenizer_sha256") != tokenizer_hash):
        raise ValueError("pilot summary does not describe exact source checkpoint")

    pools, pool_hashes = select_training_pools(corpus)
    out = Path(args.output)
    checkpoint_path = out / "last.pt"
    if out.exists() and not checkpoint_path.is_file() and any(out.iterdir()):
        raise FileExistsError("nonempty continuation output has no resumable checkpoint")
    out.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(out).free < 8 * 1024**3:
        raise RuntimeError("need at least 8 GiB available for atomic AV checkpoints")

    model = PostaMulti(config, dropout=0).half().cuda()
    configure_av_trainable(model)
    optimizer = CPUMasterAdamW(
        (p for p in model.parameters() if p.requires_grad),
        lr=args.lr, loss_scale=16, max_grad_norm=1.0)
    initial = 0
    if checkpoint_path.is_file():
        saved = torch.load(checkpoint_path, weights_only=True,
                           map_location="cpu", mmap=True)
        initial = validated_progress(
            saved, expected_step_cap=args.steps, source_hash=source_hash,
            manifest_hash=corpus.manifest_hash, tokenizer_hash=tokenizer_hash,
            pool_hashes=pool_hashes)
        model.load_state_dict(saved["model"], strict=True)
        optimizer.load_state_dict(saved["optimizer"])
        if optimizer.steps != initial:
            raise ValueError("optimizer and checkpoint step differ")
        del saved
    else:
        model.load_state_dict(source["model"], strict=True)
    del source
    torch.cuda.reset_peak_memory_stats(device)

    for step in range(initial + 1, args.steps + 1):
        started = time.monotonic()
        video_rows = [pools["video_text"][i] for i in
                      pair_indices(len(pools["video_text"]), step)]
        speech_rows = [pools["speech_text"][i] for i in
                       pair_indices(len(pools["speech_text"]), step)]
        video = [_video_training_pair(model, corpus, tokenizer, row, config, device)
                 for row in video_rows]
        speech = [_speech_training_pair(model, corpus, tokenizer, row, config, device)
                  for row in speech_rows]
        optimizer.zero_grad()
        loss, metrics = alignment_backward(
            model, video, speech, device, optimizer,
            time_encoding="relative-centered-v1",
            window_aggregation="hierarchical-residual-v1")
        if not optimizer.step():
            raise RuntimeError("nonfinite or missing AV gradients")
        # Avoid keeping large CPU frames alive while writing a checkpoint.
        del video, speech
        record = {
            "format": FORMAT, "step": step, "step_cap": args.steps,
            "loss": loss, "metrics": metrics,
            "gradient_norm": optimizer.last_grad_norm,
            "elapsed_seconds": round(time.monotonic() - started, 2),
            "cuda_peak_allocated_mib": round(
                torch.cuda.max_memory_allocated(device) / 2**20, 1),
            "video_training_pool": len(pools["video_text"]),
            "speech_training_pool": len(pools["speech_text"]),
            "quality_verified": False, "production_promotion_allowed": False,
        }
        if step % args.save_every == 0 or step == args.steps:
            save_atomic(checkpoint_path, {
                "format": FORMAT, "step": step, "step_cap": args.steps,
                "config": asdict(config), "source_checkpoint_sha256": source_hash,
                "manifest_sha256": corpus.manifest_hash,
                "tokenizer_sha256": tokenizer_hash, "pool_sha256": pool_hashes,
                "model": model.state_dict(), "optimizer": optimizer.state_dict(),
                "quarantined_research_only": True,
                "private_local_only": True, "production_promotion_allowed": False,
            })
            record["checkpoint_saved"] = True
        print(json.dumps(record, ensure_ascii=False), flush=True)
    return {"format": FORMAT, "step": args.steps, "steps_completed": args.steps,
            "output_checkpoint": str(checkpoint_path),
            "training_pools": {k: len(v) for k, v in pools.items()},
            "quality_verified": False,
            "production_promotion_allowed": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", required=True)
    parser.add_argument("--tokenizer", required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--pilot-summary", required=True)
    parser.add_argument("--comparison", required=True)
    parser.add_argument("--output", required=True)
    parser.add_argument("--steps", type=int, default=24)
    parser.add_argument("--save-every", type=int, default=2)
    parser.add_argument("--lr", type=float, default=0.000005)
    args = parser.parse_args()
    print(json.dumps(train(args), ensure_ascii=False), flush=True)


if __name__ == "__main__":
    main()
