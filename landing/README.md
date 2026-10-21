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

## Видео на картах (Wikimedia Commons)

Самые динамичные 2,5 с каждого ролика (подбор по изменению кадра), 256×272, без звука; source/vid-credits.json.
CC BY / CC BY-SA требуют указать автора — перед публикацией лендинга нужна строка с авторами в подвале.

- img/vid/hyperlapse.mp4 — «Hyperlapse Nova Gorica 1.webm» (с 17.2 с), Primorske novice, CC BY 3.0, https://commons.wikimedia.org/wiki/File:Hyperlapse_Nova_Gorica_1.webm
- img/vid/coaster.mp4 — «Impulse Knoebels Zierer Vertical Drop Roller Coaster POV.webm» (с 60.6 с), Canobie Coaster, CC BY 3.0, https://commons.wikimedia.org/wiki/File:Impulse_Knoebels_Zierer_Vertical_Drop_Roller_Coaster_POV.webm
- img/vid/fireworks.mp4 — «Nagaoka Festival Fireworks 2015 Extra Large Miracle Starmine.ogv» (с 50.9 с), 113icecream Japanese Fireworks, CC BY 3.0, https://commons.wikimedia.org/wiki/File:Nagaoka_Festival_Fireworks_2015_Extra_Large_Miracle_Starmine.ogv
- img/vid/skate.mp4 — «Freeride Skateboarding & Downhill Slides.webm» (с 67.2 с), GravitySkateboards, CC BY 3.0, https://commons.wikimedia.org/wiki/File:Freeride_Skateboarding_%26_Downhill_Slides.webm
- img/vid/frying.mp4 — «Sassermaet frying.webm» (с 5.2 с), WhirlieWheeter, CC BY 4.0, https://commons.wikimedia.org/wiki/File:Sassermaet_frying.webm
- img/vid/waterfall.mp4 — «Waterfall in Huascarán National Park - Peru.webm» (с 26.8 с), CorraleH, CC0, https://commons.wikimedia.org/wiki/File:Waterfall_in_Huascar%C3%A1n_National_Park_-_Peru.webm
- img/vid/city.mp4 — «Alaskan Way Viaduct timelapse.webm» (с 14.9 с), jonknee, CC BY 3.0, https://commons.wikimedia.org/wiki/File:Alaskan_Way_Viaduct_timelapse.webm
- img/vid/mountains.mp4 — «Flying the Rocky Mountains- Stunning FPV Drone Footage.webm» (с 87.2 с), EagleView 🦅, CC BY 3.0, https://commons.wikimedia.org/wiki/File:Flying_the_Rocky_Mountains-_Stunning_FPV_Drone_Footage.webm
- img/vid/puppies.mp4 — «Goldendoodle and Black Lab puppies playing in slow motion.webm» (с 56.9 с), Tim Sweet, CC BY-SA 3.0, https://commons.wikimedia.org/wiki/File:Goldendoodle_and_Black_Lab_puppies_playing_in_slow_motion.webm
- img/vid/fire.mp4 — «Fire آتش 10.ogv» (с 8.0 с), Mostafameraji, CC0, https://commons.wikimedia.org/wiki/File:Fire_%D8%A2%D8%AA%D8%B4_10.ogv

Фото-коробка: source/box-photo-grey.png (zsky.ai), слои — source/photo_box.py, страница index-photo.html — source/photo_page.py.
