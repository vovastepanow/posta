# Posta Embed — первый изолированный бенчмарк EmbeddingGemma 2

**Статус:** read-only research, не production-модель. Нет обучения, изменения БД,
изменения классификации, платных API или автоматического запуска из cron.

## Актуальная схема Posta

SQL с таблицей posts(id, text, media_paths, topic_id) из внешнего примера
**не соответствует** репозиторию. Актуальные данные:

- public_posts(source_key, post_key, platform, data JSONB, first_seen, last_seen)
- public_post_topics(source_key, post_key, topic_key) — предыдущие модельные метки,
  но НЕ независимая эталонная разметка
- pool_items(user_id, image_key, topic, data JSONB) — приватные пользовательские
  данные, бенчмарк их НЕ читает

JSON содержит text, иногда captions, image_local, video_local, gallery.
Функция App.MediaPath принимает URL POSTA_MEDIA_URL/32hex.extension
и открывает файл непосредственно в POSTA_MEDIA_DIR. Скрипт повторяет эту
проверку: не обращается к внешним медиа, не принимает выход из каталога или
симлинки наружу.

## Подготовка зависимостей

Рекомендуется отдельная виртуальная среда. Актуальные библиотеки:

    python3 -m pip install -U 'sentence-transformers[image,audio,video]>=6.1.0' transformers torch numpy psycopg2-binary

## Этап 1: read-only экспорт публичных публикаций

Выполняется только там, где можно подключиться к Postgres:

    export POSTA_PG='postgres://...'
    export POSTA_MEDIA_DIR='/var/lib/posta/media'
    export POSTA_MEDIA_URL='media'
    python3 research/posta_multi/benchmark_embed.py --phase snapshot \
      --output-dir data/embeddinggemma2-benchmark --limit 2000

Это SELECT в read-only PostgreSQL транзакции. Рекомендуется дополнительно
подключаться ролью с правами только SELECT. Пароль никогда не хранится в коде.
Сохранённые snapshot.jsonl и labels_template.jsonl находятся под
игнорируемой Git папкой data/; права 0600, папка 0700.

Если в public_posts меньше 2000 записей, фиктивные записи не создаются.

## Пробный CPU-запуск (перед платным GPU)

После экспорта можно проверить загрузку весов и реальный inference без аренды GPU.
Это **только smoke**, не измерение качества и не мультимодальный бенчмарк.

В уже созданном отдельном Python venv сначала поставьте CPU PyTorch (не CUDA 13),
а затем Sentence Transformers и зависимости:

    ~/.cache/posta-embed-venv/bin/python -m pip install -U torch --index-url https://download.pytorch.org/whl/cpu
    ~/.cache/posta-embed-venv/bin/python -m pip install -U 'sentence-transformers>=6.1.0' transformers numpy

Выполняется без БД и не меняет snapshot:

    ~/.cache/posta-embed-venv/bin/python research/posta_multi/benchmark_embed.py \
      --phase benchmark --output-dir data/embeddinggemma2-benchmark \
      --text-only --max-posts 20 --max-queries 3 --device cpu

Этот режим загружает только 270M text encoder (vision_config=None,
audio_config=None). Он берёт первые 20 постов с текстом из снимка и три запроса;
не анализирует изображения или видео. Метрики Recall@100 и NDCG@10 будут null,
пока нет полного ручного gold. Показатель posts/second для 20 записей
не переносится автоматически на весь корпус и на GPU.

**После smoke** полноценный прогон делается на подходящем GPU по инструкциям ниже.
Не запускайте 733 мультимодальных документа вслепую на GTX 1060 3GB.

## Этап 2: независимый прогон нейросети

Может выполняться на арендуемой GPU-машине; Postgres здесь не требуется.
Локальный медиакеш должен быть доступен этой машине:

    python3 research/posta_multi/benchmark_embed.py --phase benchmark \
      --output-dir data/embeddinggemma2-benchmark \
      --media-dir /var/lib/posta/media --media-url media --device auto

Для включения локальных видео:

    python3 research/posta_multi/benchmark_embed.py --phase benchmark \
      --output-dir data/embeddinggemma2-benchmark \
      --media-dir /var/lib/posta/media --media-url media \
      --include-video --max-frames 4

По умолчанию это текст и фото; видео — отдельный включаемый режим.
Модель сама ограничивает видео по кадрам, не требуется вручную резать их через
ffmpeg. Используется официальный мультимодальный интерфейс с совместным
документом text+image / text+video, а НЕ усреднение векторов текста и фото.
Текст документа получает Document, запрос — SearchQuery; медиа передаются
без текстового prompt_name. Аудио пока не измеряется (text+vision 440M
конфигурация с отключённым audio encoder).

Precision — BF16 на совместимой GPU, иначе FP32, никогда FP16.
На старой GPU без BF16 может не хватить памяти для FP32.

## Метрики и реальная разметка

Артефакты теста:

- snapshot.jsonl — реальный локальный срез публичных записей
- embeddings.npz — кеш векторов, идентификаторов и отпечатка входных данных
- candidates.jsonl — первые 100 кандидатов на запрос, для ручного ревью
- metrics.json — время, скорость и качество (только при проверенном gold)

Новый labels_template.jsonl содержит 24 тематических запроса с
relevant_ids=[] и judged_complete=false.

Вручную помечайте ID подходящих публикаций из полного snapshot.
Поле judged_complete=true разрешено только если для данного запроса
проверена полнота разметки **всего тестового корпуса**, а не только топ-100
результатов тестируемой модели. Иначе Recall@100 будет завышен.
Релевантные ID должны относиться именно к текущему экспортированному срезу.

После разметки:

    python3 research/posta_multi/benchmark_embed.py --phase benchmark \
      --output-dir data/embeddinggemma2-benchmark \
      --labels /safe/path/manually-reviewed.jsonl \
      --media-dir /var/lib/posta/media --media-url media

До появления ручных complete-разметок Recall@100 и NDCG@10 равны null.
Нельзя считать метками истины существующие public_post_topics.

Задержка измеряется как время кодирования запроса и точного поиска по
NumPy-матрице; это не скорость Posta production и не ANN-SLA. Кеширование
может ускорить повторные запуски; metrics.json явно это отражает.

Ограничения первого этапа: без аудио, OCR и распознавания речи, без чтения
персональных пулов, без полноценного сравнения с действующим поисковым
конвейером. Для последнего понадобится идентичный корпус и независимая
разметка. Сырые публикации нельзя публиковать вместе с результатами тестов.

Источники:

https://gemma.ai/models/embeddinggemma/multimodal/

https://huggingface.co/google/embeddinggemma-2

https://developers.googleblog.com/en/embeddinggemma-2-the-developer-guide/
