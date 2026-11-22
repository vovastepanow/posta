"""Bounded sequential decoding of video and audio on a shared clip timeline.

This module decodes one window at a time; it does not train or infer a model.
Video timestamps are FFmpeg's resampled output-grid PTS relative to the clip,
not a promise to preserve each source frame's original VFR timestamp. Audio
is resampled to mono 16 kHz on the same requested window, with leading timing
offsets padded by FFmpeg where available. Timing and quality must be validated
for each dataset; original files are never modified.
"""
from fractions import Fraction
import json
import math
import re
import subprocess

import numpy as np


_PTS = re.compile(r'\bn:\s*\d+.*?\bpts_time:\s*([-+0-9.eE]+)')


def probe(path):
    result = subprocess.run(['ffprobe', '-v', 'error', '-show_entries',
        'format=duration,start_time:stream=codec_type,avg_frame_rate,start_time',
        '-of', 'json', str(path)], capture_output=True, timeout=30, check=True)
    data = json.loads(result.stdout)
    duration = float(data.get('format', {}).get('duration') or 0)
    if not math.isfinite(duration) or duration <= 0:
        raise ValueError('missing or invalid media duration')
    streams = data.get('streams', [])
    video = next((s for s in streams if s.get('codec_type') == 'video'), None)
    if video is None:
        raise ValueError('video stream is missing')
    try:
        source_fps = float(Fraction(video.get('avg_frame_rate', '0/1')))
    except (TypeError, ValueError, ZeroDivisionError):
        source_fps = 0.0
    return {'duration_seconds': duration, 'source_fps': source_fps,
            'has_audio_stream': any(s.get('codec_type') == 'audio' for s in streams)}


def window_plan(duration, window_seconds, fps, max_frames=256):
    if (not all(isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v)
                for v in (duration, window_seconds, fps)) or
            duration <= 0 or not 0 < window_seconds <= 8 or not 0 < fps <= 30 or
            type(max_frames) is not int or max_frames < 1 or
            math.ceil(window_seconds * fps) > max_frames):
        raise ValueError('invalid or excessive AV window budget')
    # Align interior boundaries to the requested output frame grid. This avoids
    # resetting the resampler at arbitrary fractional frame positions and
    # silently losing a frame at every segment boundary.
    grid_frames = max(1, math.ceil(duration * fps - 1e-8))
    capacity = min(max_frames, math.floor(window_seconds * fps + 1e-8))
    if capacity < 1:
        raise ValueError('window is shorter than one requested frame interval')
    count = math.ceil(grid_frames / capacity)
    base, extra = divmod(grid_frames, count)
    first = 0
    for index in range(count):
        last = first + base + (index < extra)
        start = first / fps
        end = duration if index == count-1 else last / fps
        yield start, end
        first = last


def decode_window(path, start, end, *, fps=30, image_size=224, has_audio=True,
                  sample_rate=16000, max_frames=256):
    if (not 0 <= start < end or not math.isfinite(start) or not math.isfinite(end) or
            type(image_size) is not int or not 16 <= image_size <= 224 or
            sample_rate != 16000):
        raise ValueError('invalid AV decoding parameters')
    list(window_plan(end-start, end-start, fps, max_frames))
    # Run the fps filter on the shared full-clip grid. Restarting it after an
    # input seek can lose or substitute frames when source PTS straddles a
    # window boundary. This reads the prefix again, but bounds output memory.
    first_frame = math.ceil(start * fps - 1e-8)
    last_frame = math.ceil(end * fps - 1e-8)
    command = ['ffmpeg', '-hide_banner', '-loglevel', 'info', '-nostdin', '-i', str(path),
               '-map', '0:v:0', '-vf',
               f'fps={fps},trim=start_frame={first_frame}:end_frame={last_frame},'
               f'scale={image_size}:{image_size},showinfo',
               '-fps_mode', 'passthrough', '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1']
    video = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                           timeout=120, check=True)
    frame_bytes = image_size * image_size * 3
    if len(video.stdout) % frame_bytes:
        raise ValueError('partial decoded video frame')
    count = len(video.stdout) // frame_bytes
    timestamps = [float(match) for match in _PTS.findall(video.stderr.decode('utf-8', 'replace'))]
    if not 0 < count <= max_frames or len(timestamps) != count:
        raise ValueError(f'video frame or output timestamp count invalid: frames={count}, timestamps={len(timestamps)}')
    if (not all(math.isfinite(t) and start - 1e-5 <= t < end + 1e-5 for t in timestamps) or
            any(b <= a for a, b in zip(timestamps, timestamps[1:]))):
        raise ValueError('video output timestamps are outside or unordered in the requested window')
    frames = np.frombuffer(video.stdout, dtype=np.uint8).copy().reshape(count, image_size, image_size, 3)
    audio = np.empty(0, dtype=np.float32)
    if has_audio:
        # Decode from the beginning so a container's coarse seek timebase cannot
        # shift samples independently at every boundary. The trim is on the
        # resampled timeline and does not detect or remove intended silence.
        # This is bounded in memory but may be slow for long clips.
        first_sample, last_sample = round(start * sample_rate), round(end * sample_rate)
        command = ['ffmpeg', '-v', 'error', '-nostdin', '-i', str(path), '-map', '0:a:0',
                   '-af', f'aresample={sample_rate}:async=1:first_pts=0,'
                          f'atrim=start_sample={first_sample}:end_sample={last_sample}',
                   '-ac', '1', '-ar', str(sample_rate),
                   '-f', 'f32le', 'pipe:1']
        decoded = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                 timeout=120, check=True).stdout
        if len(decoded) % 4:
            raise ValueError('partial decoded audio sample')
        audio = np.frombuffer(decoded, dtype='<f4').copy()
        if len(audio) > math.ceil((end-start)*sample_rate) + 1024:
            raise ValueError('decoded audio exceeds requested window')
        if not np.isfinite(audio).all():
            raise ValueError('nonfinite decoded audio')
    return {'start': start, 'end': end, 'fps': fps, 'frames': frames,
            'frame_timestamps': timestamps, 'audio': audio, 'sample_rate': sample_rate,
            'audio_stream_present': bool(has_audio)}


def iter_segments(path, *, fps=30, window_seconds=2, image_size=224):
    metadata = probe(path)
    for start, end in window_plan(metadata['duration_seconds'], window_seconds, fps):
        yield decode_window(path, start, end, fps=fps, image_size=image_size,
                            has_audio=metadata['has_audio_stream'])
