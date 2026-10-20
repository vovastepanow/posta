# Лендинг Postmaker

Статическая страница без сборки: `index.html` + `img/`. Стиль — телемагазин, ценники старой ИКЕА, маскот Нэко-сан.
Черновик для обсуждения: https://claude.ai/artifact/UThD2HqfNde75KCSkSxidy (приватный, это превью той же страницы).

## Структура

| Путь | Что внутри |
|---|---|
| `index.html` | вся страница: стили и скрипт внутри, шрифты с Google Fonts |
| `img/` | картинки, которые использует страница |
| `img/neko-v1…v4.svg` | четыре варианта маскота: блаженный, сонный (полотенце онсэн), подмигивает (хатимаки, хаппи), манэки-нэко |
| `img/badge-v1…v4.svg` | значки-гравюры с головой каждого варианта; в шапке вместо буквы «о» стоит badge-v1 |
| `source/` | оригиналы генераций и лист размеров логотипа — для новых вырезок, на сайте не используются |

## Выкладка

Отдельный виртуальный хост Apache, приложение остаётся закрытым от интернета:

```sh
sudo mkdir -p /var/www/landing
sudo rsync -a --delete --exclude source --exclude README.md /var/www/html/postmaker/landing/ /var/www/landing/
sudo chown -R www-data:www-data /var/www/landing
```

Конфиг хоста, `Require ip` для `/postmaker` и certbot — см. разговор о развёртывании; кратко:
`DocumentRoot /var/www/landing`, `Options -Indexes`, `sudo certbot --apache -d домен -d www.домен`.

Кнопки «Подключить» пока только показывают шутку в нижней плашке — сбора заявок нет.

## Картинки и права

- `box-hero.jpg`, `box-wide.jpg`, `neko-figure.jpg` — сгенерированы в Krea (оригиналы в `source/`).
  Перед коммерческим использованием проверить условия своего тарифа Krea.
- `neko-*.svg` — логотипы, нарисованы по мотивам генерации, свои.
- `boo.jpg` — кадр из телепередачи (мем «Бу, испугался?»), источник Lenta.ru. У кадра есть правообладатель:
  для рекламы заменить на свободную картинку.
- `current-logo.svg` — нынешний логотип приложения (`../img/logo.svg`), для сравнения в разделе «Бренд».

Свободные картинки, которые пробовали для мемов (на странице сейчас не используются), — Wikimedia Commons,
общественное достояние: Хиросигэ, Куниёси, Луис Уэйн, Генриетта Роннер-Книп, Стейнлен, Боттичелли.

## Промпт генерации фигурки

```
Product photo of a boxed collectible vinyl figure, Japanese designer toy style
(like Pop Mart / Sonny Angel). Inside: a slim black cat character standing upright,
wearing a fluffy light-blue terry-cloth bathrobe with a tied belt and pink slippers,
holding a white enamel coffee mug with steam, calm half-closed yellow eyes, small smile.
Box: yellow cardboard with a red header reading "postmaker", large clear plastic
window with realistic reflections, hang tab with euro slot on top.
A small paper IKEA-style price tag hangs from the box on a string:
yellow paper, bold black numbers "390 ₽", Futura-like font, slightly creased.
Studio lighting, soft shadow, light grey background, shallow depth of field,
photorealistic, 4k.
```

## Бренд

- Шрифты: Rubik (логотип, заголовки, текст), Jost (ценники и шведские названия), Impact/Oswald (подписи мемов).
- Палитра по умолчанию «Телемагазин»: `#FFC20E` жёлтый, `#D52B1E` красный, `#1E5AA8` синий, `#00843D` зелёный.
  На странице есть переключатель: «Каталог 80-х», «Укиё-э», «Ночной эфир».
