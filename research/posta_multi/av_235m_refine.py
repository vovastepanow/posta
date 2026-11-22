"""Second-stage real AV training from a frozen, measured 24-step checkpoint.

This is a small *controlled experiment*, not production training. Changes
from the 24-step run: more isolated examples per epoch, 4-pair batches and a
frozen-text angular geometry objective. Previous checkpoints are never
overwritten; training and evaluation remain private on the host.
"""
import argparse
from dataclasses import asdict
import hashlib
import json
import shutil
import time
from pathlib import Path

import torch
from tokenizers import Tokenizer

from av_alignment import alignment_backward, configure_av_trainable
from av_continuation_policy import pair_indices, select_training_pools
from av_235m_continue import _text_target, _video_training_pair, _speech_training_pair
from corpus import Corpus
from cpu_master_adam import CPUMasterAdamW
from model import PostaMulti
from spec import Config, parameter_counts
from train_text import save_atomic

FORMAT = "posta-av-235m-geometry-refinement-v1"
LR = 0.000003
GEOMETRY_WEIGHT = 0.5
BATCH = 4
STEPS = 8


def sha(path):
    with Path(path).open("rb") as stream:
        return hashlib.file_digest(stream, "sha256").hexdigest()


def verified_source(checkpoint, report, *, manifest_sha, tokenizer_sha, source_sha):
    """Check measured continuation provenance and frozen reference diversity."""
    if (checkpoint.get("format") != "posta-av-235m-continuation-v1" or
            checkpoint.get("quarantined_research_only") is not True or
            checkpoint.get("private_local_only") is not True or
            checkpoint.get("production_promotion_allowed") is not False or
            checkpoint.get("step") != 24 or
            checkpoint.get("step_cap") != 24 or
            checkpoint.get("manifest_sha256") != manifest_sha or
            checkpoint.get("tokenizer_sha256") != tokenizer_sha or
            "optimizer" not in checkpoint or "model" not in checkpoint or
            not isinstance(checkpoint.get("config"), dict)):
        raise ValueError("source is not the completed, isolated 24-step AV checkpoint")
    config = Config(**checkpoint["config"])
    if parameter_counts(config)["total"] != 235179777:
        raise ValueError("architecture fingerprint differs from 235M")
    if (report.get("format") != "posta-av-235m-heldout-pool-v1" or
            report.get("checkpoint_format") != checkpoint["format"] or
            report.get("checkpoint_sha256") != source_sha or
            report.get("manifest_sha256") != manifest_sha or
            report.get("tokenizer_sha256") != tokenizer_sha or
            report.get("video_time_encoding") != "relative-centered-v1" or
            report.get("video_window_aggregation") != "hierarchical-residual-v1" or
            report.get("production_promotion_allowed") is not False):
        raise ValueError("missing matching expanded held-out source evaluation")
    for kind in ("video_text", "speech_text"):
        group = report.get("modalities", {}).get(kind, {})
        geometry = group.get("teacher_media_geometry", {})
        if (type(geometry.get("samples")) is not int or
                geometry["samples"] < 8 or
                type(geometry.get("text_interexample_cosine")) not in (int, float) or
                geometry["text_interexample_cosine"] >= .98 or
                type(geometry.get("text_interexample_cosine_std")) not in (int, float) or
                geometry["text_interexample_cosine_std"] <= .002):
            raise ValueError(f"{kind}: text reference is too collapsed or held-out pool too small")
    return config


def validated_refinement(state, settings):
    if (state.get("format") != FORMAT or
            state.get("settings") != settings or
            type(state.get("step")) is not int or
            not 1 <= state["step"] <= settings["step_cap"] or
            state.get("quarantined_research_only") is not True or
            state.get("private_local_only") is not True or
            state.get("production_promotion_allowed") is not False or
            "optimizer" not in state or "model" not in state):
        raise ValueError("refinement checkpoint cannot be resumed with changed dataset or objective")
    return state["step"]


def train(args):
    if not torch.cuda.is_available():
        raise RuntimeError("isolated 235M refinement requires local CUDA")
    torch.set_num_threads(2)
    torch.cuda.set_per_process_memory_fraction(.45)
    torch.manual_seed(230521)
    corpus = Corpus(args.dataset)
    tokenizer_sha = sha(args.tokenizer)
    source_sha = sha(args.source)
    source = torch.load(args.source, weights_only=True, map_location="cpu", mmap=True)
    reference = json.loads(Path(args.source_evaluation).read_text(encoding="utf-8"))
    config = verified_source(source, reference, manifest_sha=corpus.manifest_hash,
                             tokenizer_sha=tokenizer_sha, source_sha=source_sha)
    tokenizer = Tokenizer.from_file(str(args.tokenizer))
    tokenizer.enable_truncation(max_length=128)
    for special, value in (("[PAD]", 0), ("[CLS]", 1), ("[SEP]", 2)):
        if tokenizer.token_to_id(special) != value:
            raise ValueError("tokenizer special IDs mismatch")
    pools, pool_sha = select_training_pools(corpus, pool_sizes=(128, 64, 32))
    if any(len(rows) < BATCH for rows in pools.values()):
        raise ValueError("insufficient independent training media for 4-example batches")
    settings = {
        "source_checkpoint_sha256": source_sha,
        "source_evaluation_sha256": sha(args.source_evaluation),
        "manifest_sha256": corpus.manifest_hash, "tokenizer_sha256": tokenizer_sha,
        "pool_sha256": pool_sha, "pool_sizes": {k: len(v) for k, v in pools.items()},
        "batch_size_per_modality": BATCH, "step_cap": STEPS,
        "objective": "positive-alignment-plus-teacher-chord-geometry-v1",
        "teacher_geometry_weight": GEOMETRY_WEIGHT, "learning_rate": LR,
        "video_time_encoding": "relative-centered-v1",
        "video_window_aggregation": "hierarchical-residual-v1",
    }

    output = Path(args.output)
    last = output / "last.pt"
    if output.exists() and not last.is_file() and any(output.iterdir()):
        raise FileExistsError("refinement output exists but no resumable checkpoint")
    output.mkdir(parents=True, exist_ok=True)
    if shutil.disk_usage(output).free < 8 * 1024**3:
        raise RuntimeError("at least 8 GiB needed for atomic 235M refinement checkpoints")

    model = PostaMulti(config, dropout=0).half().cuda()
    configure_av_trainable(model)
    optimizer = CPUMasterAdamW(
        (p for p in model.parameters() if p.requires_grad),
        lr=LR, loss_scale=16, max_grad_norm=1.0)
    start = 0
    if last.exists():
        old = torch.load(last, weights_only=True, map_location="cpu", mmap=True)
        start = validated_refinement(old, settings)
        model.load_state_dict(old["model"], strict=True)
        optimizer.load_state_dict(old["optimizer"])
        if optimizer.steps != start:
            raise ValueError("refinement optimizer history differs from step")
        del old
    else:
        model.load_state_dict(source["model"], strict=True)
    del source
    torch.cuda.reset_peak_memory_stats()

    for step in range(start + 1, STEPS + 1):
        tic = time.monotonic()
        videos = [_video_training_pair(model, corpus, tokenizer, pools["video_text"][i],
                                       config, "cuda")
                  for i in pair_indices(len(pools["video_text"]), step, batch_size=BATCH)]
        speeches = [_speech_training_pair(model, corpus, tokenizer, pools["speech_text"][i],
                                          config, "cuda")
                    for i in pair_indices(len(pools["speech_text"]), step, batch_size=BATCH)]
        optimizer.zero_grad()
        loss, diagnostics = alignment_backward(
            model, videos, speeches, "cuda", optimizer,
            time_encoding="relative-centered-v1",
            window_aggregation="hierarchical-residual-v1",
            teacher_geometry_weight=GEOMETRY_WEIGHT)
        if not optimizer.step():
            raise RuntimeError("nonfinite AV refinement gradients; previous checkpoint intact")
        del videos, speeches
        saved = step % 2 == 0 or step == STEPS
        if saved:
            save_atomic(last, {
                "format": FORMAT, "settings": settings, "step": step,
                "config": asdict(config), "model": model.state_dict(),
                "optimizer": optimizer.state_dict(),
                "quarantined_research_only": True,
                "private_local_only": True, "production_promotion_allowed": False,
            })
        print(json.dumps({
            "format": FORMAT, "step": step, "steps": STEPS,
            "loss": loss, "diagnostics": diagnostics,
            "gradient_norm": optimizer.last_grad_norm,
            "checkpoint_saved": saved, "elapsed_seconds": round(time.monotonic() - tic, 1),
            "cuda_peak_allocated_mib": round(torch.cuda.max_memory_allocated()/2**20, 1),
            "production_promotion_allowed": False,
        }, allow_nan=False), flush=True)
    return {"format": FORMAT, "steps_completed": STEPS,
            "output_checkpoint": str(last), "source_unchanged": True,
            "production_promotion_allowed": False, "quality_verified": False}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--dataset", required=True)
    parser.add_argument("--tokenizer", required=True)
    parser.add_argument("--source", required=True)
    parser.add_argument("--source-evaluation", required=True)
    parser.add_argument("--output", required=True)
    args = parser.parse_args()
    print(json.dumps(train(args), allow_nan=False), flush=True)


if __name__ == "__main__":
    main()
