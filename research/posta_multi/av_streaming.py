"""Bounded-memory, sequential FFmpeg video/audio decoding on one clip grid.

Two read-only decoders run once per clip. Their pipes are drained concurrently
through bounded queues; no source files, silence or content are modified.
This is a research decoder and does not certify original AV sync or VFR PTS.
"""
import queue
import math
import re
import subprocess
import threading
from collections import deque
from itertools import chain

import numpy as np

from av_segments import probe, window_plan

_END = object()
_FRAME_PTS = re.compile(r'\bn:\s*\d+.*?\bpts:\s*(-?\d+)\s+pts_time:')


class _PipeReader:
    def __init__(self, command, unit_size, *, fps=None):
        self.process = subprocess.Popen(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
        self.stop = threading.Event()
        self.chunks = queue.Queue(maxsize=4)
        self.pts = queue.Queue(maxsize=16) if fps is not None else None
        self.fps = fps
        self.errors = deque(maxlen=12)
        self.unit_size = unit_size
        self.buffer = bytearray()
        self.ended = False
        self.threads = [threading.Thread(target=self._stdout, daemon=True),
                        threading.Thread(target=self._stderr, daemon=True)]
        for thread in self.threads:
            thread.start()

    def _put(self, target, item):
        while not self.stop.is_set():
            try:
                target.put(item, timeout=.2)
                return
            except queue.Full:
                pass

    def _stdout(self):
        try:
            while not self.stop.is_set():
                block = self.process.stdout.read(self.unit_size)
                if not block:
                    break
                self._put(self.chunks, block)
        finally:
            self._put(self.chunks, _END)

    def _stderr(self):
        try:
            for raw in self.process.stderr:
                line = raw.decode('utf-8', 'replace')
                if self.pts is not None:
                    match = _FRAME_PTS.search(line)
                    if match:
                        # fps filter outputs time_base=1/fps; integer PTS avoid
                        # rounding in showinfo's printed decimal pts_time.
                        self._put(self.pts, int(match.group(1)) / self.fps)
                self.errors.append(line[-300:])
        finally:
            if self.pts is not None:
                self._put(self.pts, _END)

    def read(self, count):
        while len(self.buffer) < count and not self.ended:
            try:
                part = self.chunks.get(timeout=120)
            except queue.Empty as exc:
                raise TimeoutError('FFmpeg output stalled') from exc
            if part is _END:
                self.ended = True
                try:
                    status = self.process.wait(timeout=5)
                except subprocess.TimeoutExpired as exc:
                    raise TimeoutError('FFmpeg did not finish after closing output') from exc
                if status != 0:
                    raise RuntimeError('FFmpeg decoding failed')
            else:
                self.buffer.extend(part)
        result = bytes(self.buffer[:count])
        del self.buffer[:count]
        return result

    def next_pts(self):
        try:
            value = self.pts.get(timeout=120)
        except queue.Empty as exc:
            raise TimeoutError('FFmpeg frame timestamps stalled') from exc
        if value is _END:
            raise ValueError('missing video frame timestamp')
        return value

    def close(self):
        self.stop.set()
        if self.process.poll() is None:
            self.process.terminate()
        try:
            self.process.wait(timeout=.5)
        except subprocess.TimeoutExpired:
            self.process.kill()
            self.process.wait(timeout=5)
        for thread in self.threads:
            thread.join(timeout=2)
        self.process.stdout.close()
        self.process.stderr.close()


def iter_segments_streaming(path, *, fps=30, window_seconds=2, image_size=224):
    if type(image_size) is not int or not 16 <= image_size <= 224:
        raise ValueError('invalid image size')
    metadata = probe(path)
    windows = iter(window_plan(metadata['duration_seconds'], window_seconds, fps))
    windows = chain((next(windows),), windows)  # Validate budget before starting decoders.
    frame_bytes = image_size * image_size * 3
    video_command = ['ffmpeg', '-hide_banner', '-loglevel', 'info', '-nostdin', '-i', str(path),
                     '-map', '0:v:0', '-vf', f'fps={fps},scale={image_size}:{image_size},showinfo',
                     '-fps_mode', 'passthrough', '-pix_fmt', 'rgb24', '-f', 'rawvideo', 'pipe:1']
    audio_command = ['ffmpeg', '-v', 'error', '-nostdin', '-i', str(path), '-map', '0:a:0',
                     '-af', 'aresample=16000:async=1:first_pts=0', '-ac', '1', '-ar', '16000',
                     '-f', 'f32le', 'pipe:1']
    video = _PipeReader(video_command, frame_bytes, fps=fps)
    audio = None
    try:
        if metadata['has_audio_stream']:
            audio = _PipeReader(audio_command, 16384)
        pending = None
        for start, end in windows:
            frames, times = [], []
            first_index = math.ceil(start * fps - 1e-8)
            last_index = math.ceil(end * fps - 1e-8)
            while True:
                if pending is None:
                    frame = video.read(frame_bytes)
                    if not frame:
                        break
                    if len(frame) != frame_bytes:
                        raise ValueError('partial video frame')
                    pending = (frame, video.next_pts())
                frame, timestamp = pending
                index = round(timestamp * fps)
                if index >= last_index:
                    break
                if index < first_index or (times and timestamp <= times[-1]):
                    raise ValueError('video timestamps outside window or unordered')
                frames.append(np.frombuffer(frame, dtype=np.uint8).copy().reshape(image_size, image_size, 3))
                times.append(timestamp)
                pending = None
            if not frames:
                raise ValueError('empty video window')
            first_sample, last_sample = round(start * 16000), round(end * 16000)
            raw_audio = audio.read((last_sample - first_sample) * 4) if audio else b''
            if len(raw_audio) % 4:
                raise ValueError('partial audio sample')
            samples = np.frombuffer(raw_audio, dtype='<f4').copy()
            if not np.isfinite(samples).all():
                raise ValueError('nonfinite audio')
            yield {'start': start, 'end': end, 'fps': fps, 'frames': np.stack(frames),
                   'frame_timestamps': times, 'audio': samples, 'sample_rate': 16000,
                   'audio_stream_present': metadata['has_audio_stream']}
    finally:
        video.close()
        if audio:
            audio.close()
