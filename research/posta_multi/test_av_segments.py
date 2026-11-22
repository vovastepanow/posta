import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

import numpy as np
from av_segments import decode_window, iter_segments, probe, window_plan
from av_streaming import iter_segments_streaming


class WindowPlanTests(unittest.TestCase):
    def test_entire_clip_and_bounds(self):
        windows = list(window_plan(5.2, 2, 30))
        self.assertEqual(len(windows), 3)
        self.assertEqual(windows[0][0], 0.0)
        self.assertEqual(windows[-1][1], 5.2)
        self.assertTrue(all(0 < b-a <= 2 for a, b in windows))
        for duration, window, fps in ((1, 9, 30), (1, 2, 31), (float('nan'), 2, 30), (1, 8, 60)):
            with self.subTest(duration=duration, window=window, fps=fps):
                with self.assertRaises(ValueError):
                    list(window_plan(duration, window, fps))

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
    def test_real_decode_full_video_audio_and_timestamps(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'sample.mp4'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                '-i', 'testsrc2=size=32x32:rate=10:duration=3.5', '-f', 'lavfi',
                '-i', 'sine=frequency=440:sample_rate=16000:duration=3.5',
                '-c:v', 'mpeg4', '-c:a', 'aac', '-shortest', '-y', str(path)],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
            metadata = probe(path)
            self.assertTrue(metadata['has_audio_stream'])
            windows = list(iter_segments(path, fps=5, window_seconds=2, image_size=32))
            self.assertEqual(len(windows), 2)
            self.assertGreaterEqual(len(windows[0]['frames']), 8)
            self.assertGreater(len(windows[1]['frames']), 0)
            self.assertAlmostEqual(windows[0]['frame_timestamps'][0], 0, places=3)
            self.assertAlmostEqual(windows[1]['frame_timestamps'][0], 1.8, places=3)
            self.assertGreater(len(windows[0]['audio']), 26000)
            self.assertGreater(len(windows[1]['audio']), 20000)
            self.assertAlmostEqual(windows[-1]['end'], metadata['duration_seconds'], places=3)
            with self.assertRaises(ValueError):
                decode_window(path, 0, 2, fps=30, image_size=32, max_frames=30)

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
    def test_delayed_audio_keeps_leading_silence(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'delayed.mp4'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                '-i', 'testsrc2=size=32x32:rate=10:duration=2', '-itsoffset', '0.5',
                '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=16000:duration=1.5',
                '-c:v', 'mpeg4', '-c:a', 'aac', '-y', str(path)],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
            audio = decode_window(path, 0, 2, fps=5, image_size=32)['audio']
            self.assertLess(float(np.sqrt(np.mean(audio[:3200] ** 2))), .001)
            self.assertGreater(float(np.sqrt(np.mean(audio[12800:16000] ** 2))), .01)
            streamed = np.concatenate([segment['audio'] for segment in
                                       iter_segments_streaming(path, fps=5, window_seconds=1, image_size=32)])
            np.testing.assert_array_equal(streamed, audio)

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
    def test_intentional_silence_in_audio_is_preserved(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'intentional_silence.mkv'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                '-i', 'testsrc2=size=32x32:rate=10:duration=2', '-f', 'lavfi',
                '-i', r'aevalsrc=if(lt(t\,0.5)\,0\,0.2*sin(2*PI*440*t)):s=16000:d=2',
                '-c:v', 'mpeg4', '-c:a', 'pcm_s16le', '-y', str(path)],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
            audio = decode_window(path, 0, 2, fps=5, image_size=32)['audio']
            self.assertLess(float(np.sqrt(np.mean(audio[:6400] ** 2))), .001)
            self.assertGreater(float(np.sqrt(np.mean(audio[12800:16000] ** 2))), .01)

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
    def test_segmented_decoding_matches_whole_constant_rate_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'grid.mkv'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                '-i', 'testsrc2=size=32x32:rate=30:duration=5', '-f', 'lavfi',
                '-i', 'sine=frequency=440:sample_rate=16000:duration=5',
                '-c:v', 'ffv1', '-c:a', 'pcm_s16le', '-y', str(path)],
                check=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=30)
            whole = decode_window(path, 0, 5, fps=30, image_size=32)
            pieces = list(iter_segments(path, fps=30, window_seconds=2, image_size=32))
            frames = np.concatenate([part['frames'] for part in pieces])
            audio = np.concatenate([part['audio'] for part in pieces])
            times = np.concatenate([part['frame_timestamps'] for part in pieces])
            np.testing.assert_array_equal(frames, whole['frames'])
            np.testing.assert_allclose(times, whole['frame_timestamps'], atol=2e-6)
            np.testing.assert_array_equal(audio, whole['audio'])
            streaming = list(iter_segments_streaming(path, fps=30, window_seconds=2, image_size=32))
            self.assertEqual(len(streaming), len(pieces))
            for reference, actual in zip(pieces, streaming):
                np.testing.assert_array_equal(actual['frames'], reference['frames'])
                np.testing.assert_array_equal(actual['audio'], reference['audio'])
                np.testing.assert_allclose(actual['frame_timestamps'], reference['frame_timestamps'], atol=1e-5)

    @unittest.skipUnless(shutil.which('ffmpeg') and shutil.which('ffprobe'), 'FFmpeg required')
    def test_noninteger_duration_has_one_timestamp_per_output_frame(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'fractional.mp4'
            subprocess.run(['ffmpeg', '-v', 'error', '-nostdin', '-f', 'lavfi',
                '-i', 'testsrc2=size=32x32:rate=24:duration=4.7', '-c:v', 'mpeg4',
                '-y', str(path)], check=True, stdout=subprocess.PIPE,
                stderr=subprocess.PIPE, timeout=30)
            windows = list(iter_segments(path, fps=30, window_seconds=2, image_size=32))
            self.assertGreaterEqual(len(windows), 2)
            for window in windows:
                self.assertEqual(len(window['frames']), len(window['frame_timestamps']))
            streaming = list(iter_segments_streaming(path, fps=30, window_seconds=2, image_size=32))
            np.testing.assert_array_equal(np.concatenate([x['frames'] for x in streaming]),
                                          np.concatenate([x['frames'] for x in windows]))
            off_grid = decode_window(path, .11, 1.11, fps=30, image_size=32, has_audio=False)
            self.assertGreaterEqual(off_grid['frame_timestamps'][0], .11)
            self.assertLess(off_grid['frame_timestamps'][-1], 1.11)


if __name__ == '__main__':
    unittest.main()
