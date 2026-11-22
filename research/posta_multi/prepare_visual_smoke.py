#!/usr/bin/env python3
"""Build a tiny image-only/mixed Posta benchmark from existing public-post URLs.

Reads an already exported snapshot; downloads at most N images to a separate
private folder, without touching the DB, original snapshot, or production cache.
HTTPS only; exact allow-listed public media hosts; redirects disabled; size,
format and pixel limits. Does not collect video in this first visual smoke test.
"""
from __future__ import annotations

import argparse
from collections import Counter, defaultdict, deque
import hashlib
from io import BytesIO
import json
import os
from pathlib import Path
import re
from urllib import error, request
from urllib.parse import urlsplit

from benchmark_embed import caption, label_template, read_jsonl, write_jsonl
from audit_retrieval_corpus import candidate_media, PLACEHOLDER

MAX_BYTES = 8 * 1024 * 1024
MAX_PIXELS = 20_000_000
ALLOWED_HOSTS = frozenset({
    "cdn1.telesco.pe", "cdn2.telesco.pe", "cdn3.telesco.pe",
    "cdn4.telesco.pe", "files.mastodon.social", "leonardo.osnova.io",
    "kolesa-uploads.ru", "cs16.pikabu.ru",
})
VK_HOST = re.compile(r"^(?:sun\d+-\d+|sun\d+)\.vkuserphoto\.ru$")
MEDIA_NAME = re.compile(r"^[0-9a-f]{32}\.jpg$")


def allowed_url(raw: str) -> bool:
    try:
        uri = urlsplit(raw)
        host = uri.hostname
        return (
            uri.scheme == "https"
            and bool(host)
            and (host.lower() in ALLOWED_HOSTS or bool(VK_HOST.fullmatch(host.lower())))
            and not uri.username and not uri.password and uri.port in (None, 443)
            and not uri.fragment
        )
    except ValueError:
        return False


class NoRedirect(request.HTTPRedirectHandler):
    def redirect_request(self, req, fp, code, msg, headers, newurl):
        return None


def open_remote(url: str, timeout: float, opener=None) -> bytes:
    """Small HTTPS download with no redirects (including CDN to private IP)."""
    if not allowed_url(url):
        raise ValueError("URL host, scheme or port not allowed")
    opener = opener or request.build_opener(NoRedirect)
    req = request.Request(url, headers={
        "User-Agent": "Posta-Research-VisualSmoke/1.0",
        "Accept": "image/jpeg,image/png,image/webp,image/gif,*/*;q=0.1",
    })
    with opener.open(req, timeout=timeout) as resp:
        length = resp.headers.get("Content-Length")
        if length:
            try:
                if int(length) > MAX_BYTES:
                    raise ValueError("Image content length exceeds cap")
            except ValueError as exc:
                if str(exc) == "Image content length exceeds cap":
                    raise
        chunks = []
        total = 0
        while True:
            part = resp.read(min(64 * 1024, MAX_BYTES + 1 - total))
            if not part:
                break
            total += len(part)
            if total > MAX_BYTES:
                raise ValueError("Image exceeds size cap")
            chunks.append(part)
    return b"".join(chunks)


def normalized_jpg(raw: bytes) -> bytes:
    """Decode content, reject huge/decompression-bomb images, write one static JPEG."""
    from PIL import Image, ImageOps, UnidentifiedImageError
    Image.MAX_IMAGE_PIXELS = MAX_PIXELS
    with Image.open(BytesIO(raw)) as src:
        width, height = src.size
        if width <= 0 or height <= 0 or width * height > MAX_PIXELS:
            raise ValueError("Image resolution exceeds cap")
        if src.format not in {"JPEG", "PNG", "WEBP", "GIF"}:
            raise ValueError("Unsupported image format")
        # Animated images are represented by their first frame in this smoke.
        src.seek(0)
        img = ImageOps.exif_transpose(src.convert("RGB"))
        img.thumbnail((1600, 1600), Image.Resampling.LANCZOS)
        buf = BytesIO()
        img.save(buf, format="JPEG", quality=85, optimize=True)
        return buf.getvalue()


def candidates_round_robin(rows: list[dict]) -> list[tuple[dict, str]]:
    """Spread images over sources, prioritizing captions with no useful text."""
    groups: dict[str, list[tuple[dict, str]]] = defaultdict(list)
    for row in rows:
        data = row.get("data", {})
        if not isinstance(data, dict):
            continue
        imgs, _ = candidate_media(data)
        url = next((u for u in imgs if allowed_url(u)), None)
        if url:
            groups[str(row.get("source_key", "unknown"))].append((row, url))
    for entries in groups.values():
        entries.sort(key=lambda pair: (not bool(PLACEHOLDER.fullmatch(caption(pair[0]["data"]).strip())), pair[0]["id"]))
    queue = deque((source, deque(entries)) for source, entries in sorted(groups.items()))
    ordered = []
    while queue:
        source, entries = queue.popleft()
        ordered.append(entries.popleft())
        if entries:
            queue.append((source, entries))
    return ordered


def prepare(source: Path, dest: Path, limit: int, attempts: int, timeout: float,
            fetcher=None) -> dict:
    source = source.resolve()
    dest = dest.resolve()
    if source.parent == dest or source == dest / "snapshot.jsonl":
        raise ValueError("Never overwrite original Posta snapshot")
    rows = read_jsonl(source)
    targets = candidates_round_robin(rows)
    dest.mkdir(parents=True, exist_ok=True, mode=0o700)
    dest.chmod(0o700)
    media_dir = dest / "media"
    media_dir.mkdir(exist_ok=True, mode=0o700)
    media_dir.chmod(0o700)
    fetcher = fetcher or open_remote
    selected = []
    failures = Counter()
    seen_urls, seen_contents = set(), set()
    tried = 0
    for row, url in targets:
        if len(selected) >= limit or tried >= attempts:
            break
        if url in seen_urls:
            continue
        seen_urls.add(url)
        tried += 1
        try:
            jpeg = normalized_jpg(fetcher(url, timeout))
            digest = hashlib.sha256(jpeg).hexdigest()
            if digest in seen_contents:
                failures["duplicate_image"] += 1
                continue
            seen_contents.add(digest)
            filename = digest[:32] + ".jpg"
            assert MEDIA_NAME.fullmatch(filename)
            target = media_dir / filename
            if not target.exists():
                # Exclusive creation and 0600 permissions; no network data in Git.
                with target.open("xb") as f:
                    f.write(jpeg)
            target.chmod(0o600)
            copied = dict(row)
            copied["data"] = dict(row["data"])
            copied["data"]["image_local"] = "media/" + filename
            # A placeholder must not contaminate an actual image embedding.
            if PLACEHOLDER.fullmatch(caption(copied["data"]).strip()):
                copied["data"]["text"] = ""
            selected.append(copied)
            print(f"image {len(selected)}/{limit} from {row.get('source_key','?')}", flush=True)
        except (OSError, ValueError, error.URLError, TimeoutError) as exc:
            failures[type(exc).__name__] += 1
            # Do not echo source URLs or any private data.
    if not selected:
        raise RuntimeError(f"No media downloaded from {tried} attempted URLs. Failures: {dict(failures)}")
    write_jsonl(dest / "snapshot.jsonl", selected)
    label_template(dest / "labels_template.jsonl")
    result = {
        "source_posts": len(rows),
        "download_attempts": tried,
        "image_posts": len(selected),
        "distinct_sources": len(set(p.get("source_key") for p in selected)),
        "download_failures": dict(failures),
        "saved_snapshot": str(dest / "snapshot.jsonl"),
        "saved_media_directory": str(media_dir),
        "note": "Separate private visual smoke dataset. No original DB or snapshot changes. First frame only for animated GIF/WebP. Not a quality benchmark.",
    }
    report = dest / "prepare_report.json"
    report.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    report.chmod(0o600)
    print(json.dumps(result, ensure_ascii=False, indent=2))
    return result


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", default="data/embeddinggemma2-benchmark/snapshot.jsonl")
    parser.add_argument("--output-dir", default="data/embeddinggemma2-visual-smoke")
    parser.add_argument("--limit", type=int, default=12)
    parser.add_argument("--attempts", type=int, default=36)
    parser.add_argument("--timeout", type=float, default=8)
    args = parser.parse_args()
    if not 1 <= args.limit <= 50 or not 1 <= args.attempts <= 150 or args.attempts < args.limit:
        parser.error("limit must be 1..50 and attempts must be between limit and 150")
    if not 1 <= args.timeout <= 30:
        parser.error("timeout must be 1..30")
    prepare(Path(args.source), Path(args.output_dir), args.limit, args.attempts, args.timeout)


if __name__ == "__main__":
    main()
