#!/usr/bin/env python3
"""Make a local review contact sheet of Posta's downloaded visual smoke images.

Uses only the private benchmark snapshot and files, never requests remote URLs.
No model, database, or background processing. Save the contact sheet locally and
share it for human evaluation if desired. Raw remote media URLs are never printed.
"""
from __future__ import annotations

import argparse
from pathlib import Path
import re
import textwrap

from benchmark_embed import caption, read_jsonl

MEDIA_NAME = re.compile(r"^[a-f0-9]{32}\.jpg$")


def safe_image_path(row: dict, folder: Path) -> Path | None:
    data = row.get("data", {})
    if not isinstance(data, dict):
        return None
    url = data.get("image_local")
    if not isinstance(url, str) or not url.startswith("media/"):
        return None
    name = url[len("media/"):]
    if not MEDIA_NAME.fullmatch(name):
        return None
    base = (folder / "media").resolve()
    path = base / name
    try:
        resolved = path.resolve(strict=True)
        if resolved.parent == base and resolved.is_file():
            return resolved
    except OSError:
        pass
    return None


def font(size: int):
    from PIL import ImageFont
    for candidate in (
        "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
        "/usr/share/fonts/truetype/liberation2/LiberationSans-Regular.ttf",
    ):
        try:
            return ImageFont.truetype(candidate, size)
        except OSError:
            pass
    return ImageFont.load_default()


def text_lines(text: str, max_chars: int = 42, lines: int = 2) -> list[str]:
    """Simple Unicode-safe wrap to fit fixed-size caption area."""
    compact = " ".join(text.split())
    if not compact:
        return ["(нет содержательной подписи)"]
    result = textwrap.wrap(compact, width=max_chars, break_long_words=True)
    if len(result) > lines:
        result = result[:lines]
        result[-1] = result[-1].rstrip("…")[:max_chars - 1] + "…"
    return result


def generate(folder: Path, output: Path | None = None, columns: int = 3) -> tuple[Path, int]:
    from PIL import Image, ImageOps, ImageDraw

    folder = folder.resolve()
    rows = read_jsonl(folder / "snapshot.jsonl")
    if not rows:
        raise ValueError("Visual smoke snapshot is empty")
    if not 1 <= columns <= 4:
        raise ValueError("columns must be between 1 and 4")

    # All geometry uses fixed size: numbers are stable visual IDs in snapshot order.
    width, height, gap, pad = 420, 374, 12, 18
    image_w, image_h = width - 2 * pad, 262
    count_rows = (len(rows) + columns - 1) // columns
    canvas = Image.new(
        "RGB", (columns * (width + gap) + gap, count_rows * (height + gap) + gap),
        color=(25, 27, 32),
    )
    draw = ImageDraw.Draw(canvas)
    heading, normal, small = font(22), font(15), font(13)
    loaded = 0

    for i, row in enumerate(rows):
        x = gap + i % columns * (width + gap)
        y = gap + i // columns * (height + gap)
        draw.rounded_rectangle((x, y, x + width, y + height), radius=12, fill=(43, 46, 52))
        src = safe_image_path(row, folder)
        image_box = (x + pad, y + pad + 32)
        if src is not None:
            try:
                with Image.open(src) as original:
                    # Copy happens while file handle is valid.
                    picture = ImageOps.contain(original.convert("RGB"), (image_w, image_h))
                canvas.paste(picture, (image_box[0] + (image_w - picture.width) // 2,
                                       image_box[1] + (image_h - picture.height) // 2))
                loaded += 1
            except (OSError, ValueError, Image.DecompressionBombError):
                draw.text((image_box[0], image_box[1] + 80), "INVALID IMAGE", font=normal, fill=(230, 170, 170))
        else:
            draw.text((image_box[0], image_box[1] + 80), "MISSING LOCAL IMAGE", font=normal, fill=(230, 170, 170))

        draw.text((x + pad, y + 10), f"#{i + 1:02d}", font=heading, fill=(249, 249, 250))
        source = str(row.get("source_key") or "unknown")
        source = source.removeprefix("telegram:").removeprefix("rss:").removeprefix("vk:")
        source = source[:42] + ("…" if len(source) > 42 else "")
        draw.text((x + pad, y + height - 63), source, font=small, fill=(187, 196, 207))
        for offset, line in enumerate(text_lines(caption(row.get("data") or {}), max_chars=43)):
            draw.text((x + pad, y + height - 43 + offset * 19), line, font=normal, fill=(250, 250, 250))

    target = output or folder / "contact_sheet.jpg"
    target = target.resolve()
    target.parent.mkdir(parents=True, exist_ok=True)
    canvas.save(target, "JPEG", quality=88, optimize=True)
    target.chmod(0o600)
    return target, loaded


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", default="data/embeddinggemma2-visual-smoke")
    parser.add_argument("--columns", type=int, default=3)
    args = parser.parse_args()
    target, count = generate(Path(args.output_dir), columns=args.columns)
    print(f"Contact sheet: {target}")
    print(f"Images loaded: {count}")
    print("Send ONLY the contact sheet image for visual review; keep raw snapshots local.")


if __name__ == "__main__":
    main()
