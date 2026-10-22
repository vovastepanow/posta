# Задача: перевести интерфейс приложения на дизайн «K · День / Эфир»

Ты работаешь в PHP-проекте `/var/www/html/posta` (ИИ подбирает контент по теме и публикует в Telegram/VK).
Бренд переименовывается в **Postä** (лендинг уже готов: `landing/index-posta.html`). Приложение (`index.html`,
`css/app.css`, `js/*.js`) сейчас тёмное монохромное: шрифт Golos Text, радужные рамки и спиннеры. Его нужно
перевести на дизайн **K**, который выбрал пользователь:

- **«День»** — светлый телемагазин: кремовый фон, белые карточки, чёрные обводки 2px, жёсткие тени без размытия,
  жёлтый ценник, красные звёзды-бабахи.
- **«Эфир»** — вечерний эфир: тёмный фон, карточки чуть светлее фона, приглушённые обводки, **жёлтая** жёсткая тень
  у карточек и **красная** у главных кнопок.
- Сверху бегущая строка эфира. В шапке логотип-плашка «postä» и переключатель «День | Эфир».

Это **только визуальная переделка**. Логику, API, id элементов и классы, на которые опирается JS, не меняй.

Делай шаги по порядку: 0 → 1 → … → 9. Не начинай следующий шаг, пока не прошла проверка предыдущего.

## Общие правила

- **Не делай git commit.** Пользователь проверит изменения сам.
- **Не трогай `landing/`.** Оттуда только читай (палитру, шрифты, фавиконки).
- **Не переименовывай и не удаляй** существующие id и классы: их ищут `js/*.js`. Новые классы добавлять можно.
  Прежде чем менять разметку в `index.html`, найди в `js/` все обращения к этому id или классу (`grep -n`).
- **Никаких фреймворков и сборщиков.** Чистый CSS и ES-модули, как сейчас.
- **Шрифты только локальные** (`fonts/`, `@font-face`), без подключения Google Fonts в приложении.
- **НЕ открывай сайт** (`http://localhost/...`) в curl, headless Chrome или Playwright. Каждая новая загрузка
  страницы без куки создаёт нового пользователя и запускает парсер, который нельзя остановить без sudo.
  Интерфейс проверяй **офлайн**: статическая HTML-страница в `/tmp/design-k/`, к которой подключён `css/app.css`
  (подробно в шаге 9).
- Сохрани `@media (prefers-reduced-motion: reduce)`: все новые анимации (бегущая строка, полосы прогресса,
  спиннер) при нём останавливаются.
- Контраст текста не ниже 4.5:1 в **обеих** темах, включая мелкие серые подписи. Значения в таблице шага 1 уже
  проверены. Если добавляешь свой цвет текста, проверь его.
- Комментарии в CSS — по-русски, коротко, в стиле существующего файла (`/* ---- Раздел ---- */`).

## Сначала прочитай

- `css/app.css` **целиком** (1237 строк). Особенно разделы «Палитра и основа», «Единый радужный перелив»,
  «Индикатор загрузки», «Шапка, вкладки, панели», «Раскладка», «Журнал», «Кнопки», «Поиск контента»,
  «Карточка записи», «Панель оценок».
- `css/telegram_leads.css`, `css/telegram_leads_panel.css`: там захардкожены цвета (`#4a9eff`, `#ff9800`, `#d0d0d0`
  и т.д.).
- `index.html`: `<head>`, `.app-top` (шапка и вкладки), `#activity-console`, `#content-tab`, `.controls`.
- `js/main.js` (как подключаются модули), `js/tabs.js` (вкладки), `js/progress.js` (полоса прогресса).
- `landing/index-posta.html`: блок `:root` и стили ценников (`.htag`), звёзд (`.burst`) и бегущей строки. Это
  первоисточник бренда.

## Шаг 0. Шрифты

Нужны: **Rubik** 400/500/700/900 (интерфейс), **Jost** 700/900 (ценники, цифры, логотип), **Oswald** 700
(заголовки-плашки, вкладки, бегущая строка). Подмножества cyrillic, cyrillic-ext, latin, latin-ext.

1. Скачай woff2 через CSS API Google Fonts. Чтобы получить именно woff2, нужен User-Agent современного браузера:
   ```bash
   UA='Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Safari/537.36'
   curl -s -A "$UA" 'https://fonts.googleapis.com/css2?family=Rubik:wght@400..900&family=Jost:wght@700..900&family=Oswald:wght@700&display=swap' > /tmp/design-k/fonts.css
   ```
   В `fonts.css` будут блоки `/* cyrillic */ … src: url(https://fonts.gstatic.com/…woff2)`. Скачай нужные файлы в
   `fonts/` с понятными именами по образцу существующих: `Rubik-cyrillic.woff2`, `Jost-latin.woff2`,
   `Oswald-cyrillic-ext.woff2` и т.д. Rubik и Jost вариативные, поэтому на каждое подмножество — один файл.
2. Замени в начале `app.css` блоки `@font-face` для Golos Text и Montserrat Alternates на блоки для Rubik, Jost и
   Oswald в том же формате (`font-weight: 400 900`, `font-display: swap`, тот же `unicode-range`, что отдал Google).
   Поправь комментарий раздела: «Rubik — интерфейс, Jost — ценники и логотип, Oswald — эфирные плашки (SIL OFL 1.1)».
3. В `index.html` замени `<link rel="preload" … GolosText-cyrillic.woff2>` на `Rubik-cyrillic.woff2`.
4. Старые файлы Golos и Montserrat в `fonts/` **не удаляй**: пользователь решит сам. Просто убери все ссылки на них.

**Проверка:** `grep -rn "Golos\|Montserrat" css/ index.html js/` ничего не находит. Все файлы, на которые
ссылаются `src:` новых `@font-face`, существуют (`ls fonts/`).

## Шаг 1. Токены и две темы

Тема хранится в атрибуте `data-theme` на `<html>`: `"day"` или `"night"`. Без атрибута действует «День».
Все существующие переменные сохраняют свои **имена**: на них опирается весь файл. Меняются только значения.
Добавляются новые переменные.

Перепиши блок `:root` в разделе «Палитра и основа» так:

| Переменная | День (`:root`, `[data-theme="day"]`) | Эфир (`[data-theme="night"]`) | Назначение |
|---|---|---|---|
| `--bg` | `#FFF4D6` | `#14151C` | фон страницы |
| `--surface` | `#FFFFFF` | `#1F2129` | карточки, панели, журнал |
| `--surface-2` | `#FFF9EA` | `#262833` | вложенные блоки, активный пункт меню |
| `--surface-3` | `#F3E9CC` | `#2C2E38` | наведение |
| `--line` | `#E2D8BC` | `#2C2E38` | тонкие разделители (1px) |
| `--line-strong` | `#111111` | `#3A3C48` | обводки карточек, кнопок, полей (2px) |
| `--text` | `#111111` | `#F5F2EA` | основной текст |
| `--muted` | `#54504A` | `#B4B0A8` | второстепенный текст |
| `--faint` | `#6E685F` | `#8F8B84` | подписи, время в журнале |
| `--accent` | `#FFDA1A` | `#FFDA1A` | жёлтый: главная кнопка, активная вкладка, ценник |
| `--accent-text` | `#111111` | `#111111` | текст на жёлтом (всегда чёрный) |
| `--danger` | `#C4001A` | `#FF6B6B` | ошибки, «опасные» кнопки (текстом) |
| `--red` | `#E1001A` | `#E1001A` | заливки: звёзды, плашка «В ЭФИРЕ», бегущая строка |
| `--blue` | `#1546C8` | `#1546C8` | шапки панелей-сводок, ссылки днём |
| `--link` | `#1546C8` | `#FFDA1A` | цвет ссылок |
| `--shadow-card` | `6px 6px 0 #111111` | `6px 6px 0 #FFDA1A` | жёсткая тень карточек |
| `--shadow-btn` | `3px 3px 0 #111111` | `3px 3px 0 #E1001A` | жёсткая тень главных кнопок и активной вкладки |
| `--ticker-bg` / `--ticker-text` | `#FFDA1A` / `#111111` | `#E1001A` / `#FFFFFF` | бегущая строка |
| `--ticker-tag-bg` / `--ticker-tag-text` | `#E1001A` / `#FFFFFF` | `#FFDA1A` / `#111111` | плашка в начале строки |
| `--modal-dim` | `rgba(17,17,17,0.45)` | `rgba(0,0,0,0.55)` | затемнение под модалками |

Плюс без смены по темам: `--radius: 10px; --radius-card: 14px; --border: 2px;`
`--font-ui: 'Rubik', system-ui, sans-serif; --font-tag: 'Jost', 'Futura', sans-serif;`
`--font-display: 'Oswald', Impact, 'Arial Narrow', sans-serif; --mono` оставь как есть.

Структура:
```css
:root, :root[data-theme="day"] { color-scheme: light; --bg: #FFF4D6; … }
:root[data-theme="night"] { color-scheme: dark; --bg: #14151C; … }
body { …; transition: background-color 0.3s, color 0.3s; }
```
Переменную `--rainbow` пока не удаляй: она уходит в шаге 3.

Разберись с захардкоженными цветами в `app.css` (`grep -n '#[0-9a-fA-F]\{3,8\}\b\|rgba\?(' css/app.css`):
- цвета темы (тексты, фоны панелей, рамки, `#d4d4d8` у наведения на главную кнопку и т.п.) → переменные из таблицы;
- `#000`/`rgba(0,0,0,…)` у фона просмотрщика медиа, видео и подложки под фото **оставь**: медиа всегда на чёрном;
- оранжевые/золотые `#ff9800`, `#ffd700`, `#ffb300` (архивные посты с 🔥, предупреждения) → `--accent` либо
  `--red`. Смысл сохрани: «предупреждение» = жёлтый, «ошибка» = красный.

**Проверка:** в `app.css` захардкоженных цветов осталось только для медиа и чёрного текста на жёлтом. Перечисли
оставшиеся в отчёте одной строкой.

## Шаг 2. Переключатель «День | Эфир»

1. **Без мигания при загрузке.** В `<head>` в `index.html`, **до** `<link rel="stylesheet">`, добавь маленький
   inline-скрипт. Он ставит `data-theme` до отрисовки:
   ```html
   <script>try{var t=localStorage.getItem('postaTheme');if(t!=='day'&&t!=='night'){var h=new Date().getHours();t=(h>=19||h<7)?'night':'day'}document.documentElement.dataset.theme=t}catch(e){document.documentElement.dataset.theme='day'}</script>
   ```
2. Новый модуль `js/theme.js` (подключи из `js/main.js` так же, как остальные модули):
   - режимы: выбор пользователя в `localStorage['postaTheme']` (`'day'` | `'night'`); без выбора — «авто»:
     Эфир с 19:00 до 07:00 по местному времени, иначе День. В авто-режиме перепроверяй раз в 10 минут;
   - все обращения к `localStorage` оборачивай в `try/catch`: в приватном окне он может бросить исключение;
   - при смене темы ставь `document.documentElement.dataset.theme`, обновляй `<meta name="theme-color">`
     (`#FFF4D6` / `#14151C`) и `aria-pressed` у двух кнопок;
   - двойной клик по уже активной кнопке ничего не ломает.
3. Разметка в `.app-top` рядом с логотипом:
   ```html
   <div class="theme-switch" role="group" aria-label="Оформление">
     <button type="button" data-theme-set="day" aria-pressed="true">День</button>
     <button type="button" data-theme-set="night" aria-pressed="false">Эфир</button>
   </div>
   ```
   Стиль — сегментированная «таблетка»: обводка `var(--border) solid var(--line-strong)`, `border-radius: 999px`.
   Активная кнопка днём — чёрная с жёлтым текстом, в эфире — жёлтая с чёрным текстом. Неактивная — прозрачная.
   Шрифт Rubik 700 14px. Высота кнопки не меньше 40px.

**Проверка:** в офлайн-странице из шага 9 переключение меняет всё оформление. После перезагрузки выбор
сохраняется. Если удалить ключ `postaTheme`, тема ставится по часам.

## Шаг 3. Убрать радугу

Радуга — главный признак старого дизайна. Замени все места, где используется `--rainbow` или `--tab-angle`:

| Где (раздел `app.css`) | Было | Стало |
|---|---|---|
| `:where(a[href]:hover, .rainbow-text)` | переливающийся текст | ссылки `color: var(--link)`, при наведении `text-decoration: underline 2px`. Класс `.rainbow-text` → жёлтая заливка-маркер: `background: var(--accent); color: var(--accent-text); padding: 0 .2em` |
| `.tab-btn:hover::after` | радужное кольцо | без псевдоэлемента: `border-color: var(--text)`, `transform: translate(-1px,-1px)` |
| `.tab-btn.active::after` | радужное кольцо | `::after` убрать. Сама кнопка: `background: var(--accent); color: var(--accent-text); border-color: #111; box-shadow: var(--shadow-btn)` |
| `.spinner, .is-loading::before` | радужное кольцо с вырезом | кольцо из двух цветов: `conic-gradient(var(--red) 0 90deg, var(--accent) 90deg 270deg, transparent 270deg)` с той же маской и вращением `spinner-turn`. Анимацию `tab-rainbow-spin` убрать |
| полоса поиска (~строка 212), `.vc-fill` (~1139), полоса в редакторе (~1208) | бегущий радужный градиент | жёлтая полоса с бегущими диагональными чёрными полосками, как у ценника «акция»: `background: repeating-linear-gradient(-45deg, var(--accent) 0 12px, #111 12px 24px)` при низкой непрозрачности тёмных полос (`#11111133`), анимация сдвига `background-position` |

После замены удали `--rainbow`, `@property --tab-angle`, `@keyframes tab-rainbow-spin`, `rainbow-flow`, если на них
больше никто не ссылается. `@property --spinner-angle` оставь, если он нужен спиннеру.

**Проверка:** `grep -n "rainbow\|tab-angle" css/ js/ index.html -r` — пусто либо только класс `.rainbow-text`,
если он используется в JS (проверь `grep -rn rainbow-text js/`).

## Шаг 4. Шапка: логотип, название, вкладки

1. Логотип — плашка в CSS, без картинки. Замени содержимое `.app-brand`:
   ```html
   <span class="app-brand-plate" aria-label="Postä">postä</span>
   ```
   `<img class="app-brand-logo">` и `<span class="app-brand-name">` удали, но сначала проверь `grep -rn
   "app-brand" js/`. Стиль плашки: `background: var(--accent); color: #111; font: 900 32px/1 var(--font-tag);
   letter-spacing: -0.02em; padding: 6px 14px 8px; border: var(--border) solid #111`. Без скруглений: это ценник.
2. `<title>` → `Postä`. Фавиконки: скопируй `landing/img/posta-favicon-16.png`, `-32.png`, `-180.png` в `img/` и
   поправь `<link rel="icon">` / `apple-touch-icon` в `<head>`. Старые файлы не удаляй.
3. В пользовательских текстах `index.html` и `js/account.js` замени слово «Posta» на «Postä». Заголовок
   `X-Posta` в `js/core.js`, `js/content.js` и `js/telegram_leads.js` **не трогай**: это защита от CSRF,
   его проверяет сервер.
4. Вкладки `.tab-btn`: `font: 700 16px var(--font-display); text-transform: uppercase; letter-spacing: .03em`,
   обводка `var(--border) solid var(--line-strong)`, `border-radius: var(--radius)`, фон `var(--surface)`. Из текста
   вкладок убери эмодзи (`🌳 Дерево` → `Дерево`, `🎯 Радар лидов` → `Радар лидов`). Вторичные вкладки
   (`.tabs-secondary`) — тот же стиль, мельче (13px).
5. Выпадающее меню темы (`.topic-switch-menu`): фон `var(--surface)`, обводка `var(--line-strong)` 2px, тень
   `var(--shadow-card)`. Убери полупрозрачный тёмный `rgba(21,21,23,.97)`.

## Шаг 5. Бегущая строка эфира и раскладка

1. Первым элементом `<body>`, **после** `#app-loading`, добавь:
   ```html
   <div class="air-ticker" aria-hidden="true">
     <span class="air-ticker-tag air-day">Дневной выпуск</span><span class="air-ticker-tag air-night">● Вечерний эфир</span>
     <span class="air-ticker-text air-day">Тренд живёт пару часов — ловим его за вас ★ Звоните прямо сейчас!* ★ *звонить не нужно</span>
     <span class="air-ticker-text air-night">Пока вы отдыхаете — подбор работает ★ Не переключайтесь!</span>
   </div>
   ```
   `.air-day` видно только при `data-theme="day"` (или без атрибута), `.air-night` — только при `night`.
   Стиль: `background: var(--ticker-bg); color: var(--ticker-text); font: 700 15px var(--font-display);
   text-transform: uppercase; letter-spacing: .04em; white-space: nowrap; overflow: hidden;
   border-bottom: 3px solid var(--line-strong)`; плашка — `var(--ticker-tag-*)`. Текст медленно едет справа
   налево (`@keyframes`, только `transform`). При `prefers-reduced-motion` стоит на месте.
2. **Раскладка ≥900px сейчас — грид из двух колонок на `<body>`** (раздел «Раскладка»). Строка должна занять всю
   ширину сверху: `.air-ticker { grid-column: 1 / -1; grid-row: 1; margin: -20px -20px 0; }` (компенсирует
   `padding: 20px` у body). `.app-main` и `.activity-console` переведи на `grid-row: 2`. Липкий журнал
   (`position: sticky; top: 20px; height: calc(100vh - 40px)`) не должен залезать под строку: проверь на офлайн-странице.
3. На телефоне (<900px) строка — обычный блок на всю ширину над шапкой, тоже с отрицательными боковыми отступами.
   Горизонтальной прокрутки страницы быть не должно.
4. Плашка `.tab-inactive-bar` («Приложение открыто в другой вкладке») остаётся липкой сверху и поверх строки.

## Шаг 6. Журнал = «Сводка»

`#activity-console` остаётся моноширинным журналом (длинные строки так читать удобнее), но оформляется как панель
дизайна K:
- рамка `var(--border) solid var(--line-strong)`, `border-radius: var(--radius-card)`, фон `var(--surface)`, днём
  `box-shadow: 5px 5px 0 #111`, в эфире без тени. Сейчас фон захардкожен `#080809`: замени;
- шапка `.activity-console-header`: слева плашка-индикатор. Когда у консоли класс `.is-active`, это красная пилюля
  `● В ЭФИРЕ` (`background: var(--red); color: #fff; font: 700 11px var(--font-ui); letter-spacing: .08em;
  border-radius: 4px; padding: 3px 8px`), иначе серая `Ожидание`. Сделай это стилями поверх существующего
  `.console-indicator` и `#activity-console-status` (текст статуса задаёт JS, его не трогай). Рядом заголовок
  «Сводка» шрифтом `var(--font-display)` 16px uppercase;
- время в строках журнала → `var(--faint)`, текст → `var(--muted)`, итоговые и успешные строки → `var(--text)`
  жирным. Если у журнала есть классы ошибок, им `var(--danger)`;
- ASCII-рамки уведомлений о парсинге не ломай: шрифт остаётся `var(--mono)`.

## Шаг 7. Карточки, ценник, кнопки, поля

1. **Карточки** (`.card`, карточки источников, предложки, кабинета, панели баз данных, `.controls`):
   фон `var(--surface)`, `border: var(--border) solid var(--line-strong)`, `border-radius: var(--radius-card)`,
   `box-shadow: var(--shadow-card)` — у главной карточки записи и у `.controls`. У мелких вложенных карточек тени нет,
   иначе будет каша.
2. **Ценник оценки** `#item-rating` (класс `.badge`): жёлтая табличка в правом верхнем углу карточки.
   `position: absolute; top: -14px; right: 14px; z-index: 2; background: var(--accent); color: #111;
   border: var(--border) solid #111; border-radius: 0; font: 900 26px/1 var(--font-tag); padding: 6px 10px 7px`.
   Родителю (`.card` или `.item-card-wrapper`) — `position: relative`. Другие `.badge` (в тесте ИИ и т.д.) — та же
   табличка, но в потоке (не absolute) и меньше (16px). Значок `%` можно уменьшить через `::after`, только если текст
   ставит JS без знака; иначе не трогай.
3. **Медиа-грид** `.media-shell`: отступ 12px внутри карточки, `border-radius: var(--radius)`, `overflow: hidden`,
   зазор между плитками 4px. Фон под медиа чёрный.
4. **Кнопки** (раздел «Кнопки»):
   - `.primary-button`, `.topic-row button`: `background: var(--accent); color: #111; border: var(--border) solid
     #111; box-shadow: var(--shadow-btn); font-weight: 900`. При наведении `transform: translate(-1px,-1px)` и тень
     на 1px больше, при нажатии `translate(2px,2px)` без тени;
   - `.secondary-button`, `.refresh-button`, `.account-secondary`: прозрачный фон, `border: var(--border) solid
     var(--line-strong)`, `color: var(--text)`; наведение — `background: var(--surface-3)`;
   - `.danger-button`: обводка и текст `var(--danger)`;
   - `border-radius: var(--radius)`, высота не меньше 40px.
5. **Панель оценок** `.btn`, `.btn-main`, `.btn-mini`: убери градиенты и белое свечение. Фон `var(--surface)`,
   обводка `var(--border) solid var(--line-strong)`, при наведении `translate(-2px,-2px)` и `box-shadow:
   var(--shadow-btn)`. Пружинную анимацию иконки оставь. Главные кнопки по краям (`.btn-main`) — жёлтые
   с чёрной обводкой.
6. **Поля ввода** (`input`, `select`, `textarea`, `.topic-row input`): `border: var(--border) solid
   var(--line-strong); border-radius: var(--radius); background: var(--surface); color: var(--text)`; фокус —
   `outline: 3px solid var(--accent); outline-offset: 1px`. Глобальный `:focus-visible` тоже переведи на
   `var(--accent)`, а днём на кремовом фоне — `#111` (жёлтая рамка на креме почти не видна).
7. **Чекбоксы-фишки** `.check-chip`: отмеченная — жёлтая заливка, чёрная галочка.
8. **Звезда «Свежак!»** (последним пунктом шага, если всё остальное готово). Если в карточке записи есть дата
   публикации (`#item-date`, её заполняет `js/content.js`) и посту меньше 2 часов, JS добавляет карточке класс
   `is-fresh`, а CSS рисует в левом верхнем углу красную звезду 84×84 с текстом «Свежак!» (`content: 'Свежак!'`,
   `font: 900 14px/1.05 var(--font-ui)`, белый текст, `display: grid; place-items: center`, `rotate(-12deg)`).
   Чтобы не трогать разметку, звезду делай через `::before`. Форма — 16-лучевая звезда, используй ровно этот
   многоугольник (такая же звезда, как `.burst` на лендинге):
   ```css
   clip-path: polygon(100% 50%, 89.2% 57.8%, 96.2% 69.1%, 83.3% 72.2%, 85.4% 85.4%, 72.2% 83.3%, 69.1% 96.2%, 57.8% 89.2%, 50% 100%, 42.2% 89.2%, 30.9% 96.2%, 27.8% 83.3%, 14.6% 85.4%, 16.7% 72.2%, 3.8% 69.1%, 10.8% 57.8%, 0% 50%, 10.8% 42.2%, 3.8% 30.9%, 16.7% 27.8%, 14.6% 14.6%, 27.8% 16.7%, 30.9% 3.8%, 42.2% 10.8%, 50% 0%, 57.8% 10.8%, 69.1% 3.8%, 72.2% 16.7%, 85.4% 14.6%, 83.3% 27.8%, 96.2% 30.9%, 89.2% 42.2%);
   ```
   Если дату надёжно получить нельзя, пропусти пункт и напиши об этом в отчёте.

## Шаг 8. Остальные экраны и модуль лидов

Пройди по экранам: Предложка, Источники, Дерево тем, Тест ИИ, Радар лидов, Личный кабинет (все подразделы), Базы
данных, просмотрщик медиа, редактор поста, выбор даты. Большая часть перекрасится через переменные сама. Ищи:
- захардкоженные тёмные фоны и светлые тексты, которые днём выглядят как чёрные дыры;
- `box-shadow` со светлым свечением `rgba(255,255,255,…)` → убрать либо `var(--shadow-card)`;
- в `css/telegram_leads.css` и `css/telegram_leads_panel.css` все цвета → переменные: синий `#4a9eff` →
  `var(--blue)` (днём) / `var(--link)`, оранжевый `#ff9800` → `var(--accent)` с чёрным текстом, зелёный `#4caf50`
  (успех) → оставь зелёным, но темнее для дня (`#0B7A3B`) и светлее для эфира (`#4ADE80`) через новую переменную
  `--ok`, серые `#d0d0d0`/`#e0e0e0`/`#999`/`#333` → `--line`/`--muted`/`--text`.

Заголовки разделов (`.section-title`, `h2` в панелях) можно поставить шрифтом `var(--font-display)` uppercase —
только там, где это короткие подписи («Источники темы», «Темы поиска»), а не длинные фразы.

## Шаг 9. Проверка офлайн

1. Сделай `/tmp/design-k/preview.html`: скопируй в него `<head>` из `index.html` (пути к `css/` и `fonts/` сделай
   абсолютными `file:///var/www/html/posta/...`) и **статическую** разметку `<body>` из `index.html` **без**
   `<script type="module" src="js/main.js">` и `telegram_leads.js` (иначе страница начнёт ходить в API). Вручную
   сними `hidden` с `#item-card-wrapper` и подставь в `#item-media-grid` 4 плитки-заглушки с картинками
   `file:///var/www/html/posta/landing/img/jet/*.webp` (любые 3, например `p0.webp`) и тёмную плитку с
   кнопкой ▶.
   Сделай две копии: `preview-day.html` (`<html data-theme="day">`) и `preview-night.html` (`data-theme="night"`).
2. Скриншоты (так работает на этом сервере):
   ```bash
   for t in day night; do for w in 1280 390; do
     google-chrome --headless --disable-gpu --hide-scrollbars --window-size=$w,900 \
       --screenshot=/tmp/design-k/$t-$w.png file:///tmp/design-k/preview-$t.html; done; done
   ```
   Открой все 4 PNG и посмотри сам (Read умеет показывать картинки). Проверь: строка сверху на всю ширину,
   журнал слева не налезает на неё, ценник «92%» в углу карточки, нет горизонтальной прокрутки на 390px, ни одного
   места с белым текстом на креме или чёрным на тёмном.
3. `grep -rn "rainbow\|Golos\|Montserrat\|#080809\|#0b0b0c" css/ index.html js/` — должно быть пусто (или только
   оправданные места, перечисли их).
4. `php -l index.php` и `node --check js/theme.js` — без ошибок.

## Что сдать

Короткий отчёт пользователю по-русски:
- список изменённых и новых файлов (одна строка на файл: что в нём поменялось);
- пути к 4 скриншотам из шага 9;
- что не сделано или сделано иначе и почему (например, звезда «Свежак!»);
- оставшиеся захардкоженные цвета и почему они остались.

Напомни пользователю: старые шрифты Golos и Montserrat и старые логотипы в `img/` остались на диске, их можно
удалить после проверки. Коммит он делает сам.
