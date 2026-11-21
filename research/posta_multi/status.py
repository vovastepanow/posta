"""Read saved progress from Posta Multi training logs without loading checkpoints."""
import json
from pathlib import Path

ROOT = Path('/mnt/hdd_ubuntu/posta-pmr')


def main():
    for name in ('posta-multi-mixed-pilot-v2', 'posta-multi-retrieval-pilot-v1', 'posta-multi-image-pilot-v1', 'posta-multi-audio-full-v3', 'posta-multi-audio-full-v4', 'posta-multi-audio-batch4-v5', 'posta-multi-audio-adam-v6', 'posta-multi-audio-expanded-v7', 'posta-multi-text-adam-v8', 'posta-multi-retrieval-adam-v9'):
        path = ROOT / (name + '.log')
        checkpoint = ROOT / (name + '.pt')
        last = None
        if path.exists():
            with path.open(encoding='utf-8') as f:
                for line in f:
                    try:
                        value = json.loads(line)
                        if isinstance(value, dict) and ('saved_step' in value or 'step' in value):
                            last = value
                    except json.JSONDecodeError:
                        continue
        print(json.dumps({'stage': name, 'latest_log_entry': last,
                          'checkpoint_exists': checkpoint.is_file()}, ensure_ascii=False))
    print('Progress is the most recent saved checkpoint, not live GPU activity.')


if __name__ == '__main__':
    main()
