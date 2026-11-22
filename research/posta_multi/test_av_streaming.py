import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import av_streaming
import numpy as np


@unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
class StreamingLifecycleTests(unittest.TestCase):
    def test_early_close_stops_both_decoders(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'clip.mkv'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                            '-i', 'testsrc2=size=32x32:rate=10:duration=4', '-f', 'lavfi',
                            '-i', 'sine=sample_rate=16000:duration=4', '-c:v', 'ffv1',
                            '-c:a', 'pcm_s16le', '-y', str(path)], check=True, timeout=30)
            readers = []
            original = av_streaming._PipeReader
            def record_reader(*args, **kwargs):
                reader = original(*args, **kwargs)
                readers.append(reader)
                return reader
            with patch.object(av_streaming, '_PipeReader', side_effect=record_reader):
                segments = av_streaming.iter_segments_streaming(path, fps=10, window_seconds=1, image_size=32)
                self.assertEqual(len(next(segments)['frames']), 10)
                segments.close()
            self.assertEqual(len(readers), 2)
            self.assertTrue(all(reader.process.poll() is not None for reader in readers))
            self.assertTrue(all(not thread.is_alive() for reader in readers for thread in reader.threads))

    def test_silent_opening_and_synchronized_flash_tone_across_windows(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'sync.mkv'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                '-i', 'color=black:size=32x32:rate=30:duration=4', '-f', 'lavfi',
                '-i', r'aevalsrc=if(between(t\,2.2\,2.4)\,0.4*sin(2*PI*440*t)\,0):s=16000:d=4',
                '-vf', r'drawbox=x=0:y=0:w=iw:h=ih:color=white:t=fill:enable=between(t\,2.2\,2.4)',
                '-c:v', 'ffv1', '-c:a', 'pcm_s16le', '-y', str(path)],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
            windows = list(av_streaming.iter_segments_streaming(path, fps=30, window_seconds=2, image_size=32))
            frames = np.concatenate([part['frames'] for part in windows])
            times = np.concatenate([part['frame_timestamps'] for part in windows])
            audio = np.concatenate([part['audio'] for part in windows])
            self.assertLess(float(np.max(np.abs(audio[:16000 * 2]))), .001)
            flash = int(np.flatnonzero(frames.mean(axis=(1, 2, 3)) > 128)[0])
            tone = int(np.flatnonzero(np.abs(audio) > .01)[0]) / 16000
            self.assertAlmostEqual(times[flash], 2.2, delta=1/30)
            self.assertAlmostEqual(tone, 2.2, delta=1/30)
            self.assertLessEqual(abs(times[flash] - tone), 1/30)


if __name__ == '__main__':
    unittest.main()
