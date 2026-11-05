package posta

// Карта тем Posta (редакция 2026-10-29). Правила:
//   - лист — ниша, по которой реально ведут каналы; ветка сама служит общей темой — листьев «Прочее» нет;
//   - ключ = «ветка.лист» и всегда совпадает с местом в дереве; старые ключи переводит LegacyTagKeys;
//   - форма поста (мем, видео, переписка) — отдельная ось (config.ContentFormats), а не тема;
//   - у листа одна рубрика-хэштег и до шести настоящих синонимов; персонажи и бренды — Entities.
// Порядок веток важен: модель читает список сверху вниз, 18+ и «Жесть» первыми.

type TopicFormat struct {
	Media   string   `json:"media,omitempty"`    // required | optional — нужна ли картинка или видео
	Text    string   `json:"text,omitempty"`     // required | optional — нужен ли текст
	Judge   string   `json:"judge,omitempty"`    // image | text | both — по чему судить «по теме»
	MinText int      `json:"min_text,omitempty"` // минимум символов текста при text=required
	Kinds   []string `json:"kinds,omitempty"`
}

type TreeLeaf struct {
	Key, Title, About, Hint string
	Aliases                 []string
	Rubric                  string   // хэштег-рубрика
	Synonyms                []string // синонимы рубрики (хэштеги, которые к ней сводятся)
	Entities                []string // персонажи, бренды: модель может их не знать
	Queries                 []string // готовые поисковые запросы для подбора источников
	Format                  *TopicFormat
	NSFW                    *bool
}

type TreeBranch struct {
	Key, Title, About string
	NSFW              bool
	Format            TopicFormat
	Aliases           []string
	Rubric            string
	Children          []TreeLeaf
}

var (
	fmtMedia    = TopicFormat{Media: "required", Text: "optional", Judge: "both"}
	fmtMixed    = TopicFormat{Media: "optional", Text: "optional", Judge: "both"}
	fmtText     = TopicFormat{Media: "optional", Text: "required", Judge: "text"}
	fmtVideoish = &TopicFormat{Media: "required", Text: "optional", Judge: "image"}
	fmtTextLeaf = &TopicFormat{Media: "optional", Text: "required", Judge: "text"}
	fmtLooseTxt = &TopicFormat{Media: "optional", Text: "optional", Judge: "text"}
	fmtImageLf  = &TopicFormat{Media: "required", Text: "optional", Judge: "image"}
)

// TopicTree — дерево тем.
var TopicTree = []TreeBranch{
	{Key: "adult", Title: "18+", NSFW: true, Format: fmtMedia, Rubric: "18плюс",
		About:   "Контент для взрослых: обнажённое тело, секс, откровенные шутки.",
		Aliases: []string{"для взрослых", "18+", "nsfw", "взрослое"},
		Children: []TreeLeaf{
			{Key: "adult.erotic", Title: "Эротика", About: "Откровенные фото и видео без показа секса: бельё, ню, позирование.",
				Aliases: []string{"эротика", "ню", "фап контент"}, Rubric: "эротика", Synonyms: []string{"ню", "nsfw"}},
			{Key: "adult.porn", Title: "Порно", About: "Явный секс на фото и видео.", Aliases: []string{"порно", "xxx"}, Rubric: "порно", Synonyms: []string{"xxx"}},
			{Key: "adult.hentai", Title: "Хентай", About: "Откровенные аниме-рисунки и арт 18+.", Aliases: []string{"хентай", "hentai", "rule34"}, Rubric: "хентай", Synonyms: []string{"hentai"}},
			{Key: "adult.lewd_humor", Title: "Пошлый юмор", About: "Шутки и мемы про секс и тело без откровенных кадров.",
				Hint: "Есть откровенные кадры — «Эротика» или «Порно».", Aliases: []string{"пошлые мемы", "пошлый юмор"}, Rubric: "пошлое", Synonyms: []string{"пошлыемемы", "18плюсмемы"}},
		}},
	{Key: "shock", Title: "Жесть", NSFW: true, Format: fmtMedia, Rubric: "жесть",
		About:   "Шокирующие кадры: насилие, травмы, катастрофы.",
		Aliases: []string{"жесть", "шок контент", "треш"},
		Children: []TreeLeaf{
			{Key: "shock.violence", Title: "Драки и насилие", About: "Драки, нападения, травмы, кровь.", Aliases: []string{"драки", "насилие"}, Rubric: "драки", Synonyms: []string{"драка", "насилие"}},
			{Key: "shock.accidents", Title: "Аварии и катастрофы", About: "ДТП, крушения, пожары, обрушения, стихийные бедствия в момент события.",
				Aliases: []string{"аварии", "дтп", "катастрофы"}, Rubric: "аварии", Synonyms: []string{"дтп", "катастрофа", "авария"}},
		}},
	{Key: "memes", Title: "Мемы", Format: fmtMedia, Rubric: "мемы",
		About:   "Мемы: картинка или видео с узнаваемым шаблоном или подписью-шуткой. Над чем шутят — отдельная метка.",
		Aliases: []string{"мемы", "мемасы", "мемчики", "мемы и юмор", "мемы и приколы", "мемы обо всём"},
		Children: []TreeLeaf{
			{Key: "memes.relatable", Title: "Жиза", About: "Мемы про узнаваемые бытовые ситуации: «это я», работа, учёба, отношения, интернет-типажи.",
				Aliases: []string{"жиза", "мемы про жизнь", "relatable"}, Rubric: "жиза", Synonyms: []string{"жизненно", "этоя", "relatable"}},
			{Key: "memes.absurd", Title: "Абсурд и шитпост", About: "Бессмыслица, сюр, шакальные картинки, брейнрот, мемы зумеров и альфы.",
				Hint:    "Смешно, потому что бессмысленно; понятная шутка с подписью — «Мемы» или «Жиза».",
				Aliases: []string{"абсурд", "шитпост", "брейнрот", "абсурдный юмор", "cursed", "скибиди"}, Rubric: "шитпост",
				Synonyms: []string{"абсурд", "брейнрот", "brainrot", "сюр", "cursed", "shitpost"},
				Entities: []string{"Тралалеро Тралала", "Бомбардиро Крокодило", "Скибиди туалет", "Шимпанзини Бананини", "Chill guy", "Сигма"}},
			{Key: "memes.dark", Title: "Чёрный юмор", About: "Шутки и мемы про смерть, болезни и трагедии.", Aliases: []string{"черный юмор", "чёрные мемы"},
				Rubric: "черныйюмор", Synonyms: []string{"darkhumor", "мрачныйюмор", "чернуха"}},
			{Key: "memes.wholesome", Title: "Добрые мемы", About: "Милые и тёплые мемы без злой шутки.", Aliases: []string{"добрые мемы", "wholesome"},
				Rubric: "добрыемемы", Synonyms: []string{"wholesome", "милота"}},
			{Key: "memes.topical", Title: "Мемы на злобу дня", About: "Мемы про свежие новости, политику, события и инфоповоды.",
				Aliases: []string{"мемы про новости", "политические мемы"}, Rubric: "злобадня", Synonyms: []string{"новостныемемы", "политмемы"}},
		}},
	{Key: "humor", Title: "Юмор", Format: fmtMedia, Rubric: "юмор",
		About:   "Смешное, но не мем: анекдоты, переписки, смешные видео, фейлы, пранки, стендап.",
		Aliases: []string{"юмор", "приколы", "смешное", "ржака"},
		Children: []TreeLeaf{
			{Key: "humor.jokes", Title: "Анекдоты и переписки", About: "Анекдоты, шутки текстом, смешные переписки и копипасты.",
				Hint: "Картинка с подписью-шуткой — «Мемы».", Aliases: []string{"анекдоты", "шутки", "смешные переписки", "копипасты"},
				Rubric: "анекдоты", Synonyms: []string{"шутки", "анекдот", "переписка", "копипаста"}, Format: &TopicFormat{Media: "optional", Text: "optional", Judge: "both"}},
			{Key: "humor.videos", Title: "Смешные видео и фейлы", About: "Смешные ролики из жизни, падения, неудачи, неловкие ситуации.",
				Hint: "Постановка — «Стендап и скетчи», розыгрыш — «Пранки».", Aliases: []string{"смешные видео", "фейлы", "неудачи", "приколы на видео"},
				Rubric: "смешноевидео", Synonyms: []string{"фейлы", "fail", "видеоприколы"}, Format: fmtVideoish},
			{Key: "humor.standup", Title: "Стендап и скетчи", About: "Выступления комиков, юмористические шоу, постановочные сценки.",
				Aliases: []string{"стендап", "скетчи", "камеди"}, Rubric: "стендап", Synonyms: []string{"скетчи", "standup", "комедия"}, Format: fmtVideoish},
			{Key: "humor.pranks", Title: "Пранки", About: "Розыгрыши, подставы, скрытая камера.", Aliases: []string{"пранки", "розыгрыши"},
				Rubric: "пранки", Synonyms: []string{"пранк", "prank", "розыгрыш"}, Format: fmtVideoish},
		}},
	{Key: "internet", Title: "Интернет-культура", Format: fmtMedia, Rubric: "интернет",
		About:   "Жизнь интернета: стримеры, тренды, персонажи рунета, нейросетевой контент, крипота.",
		Aliases: []string{"интернет культура", "интернет"},
		Children: []TreeLeaf{
			{Key: "internet.lower", Title: "Нижний интернет", About: "Треш-стримеры, фрик-блогеры и мемные персонажи рунета: клипы, интервью, стримы, драмы.",
				Hint:    "Про конкретных персонажей; безымянные шакальные картинки — «Абсурд и шитпост».",
				Aliases: []string{"нижний интернет", "low internet", "треш стримеры", "фрик блогеры", "хроники нижнего интернета"},
				Rubric:  "нижнийинтернет", Synonyms: []string{"трешстрим", "lowinternet"}, Format: fmtLooseTxt,
				Entities: []string{"VJLink", "Вжлинк", "Никита Литвинков", "Олег Монгол", "Гобзавр", "Людмурик", "Хилми Форкс", "Даша Корейка", "Влад Савельев",
					"Савелич", "Руки-Базуки", "Funn1k", "Николай Белов", "Пахом", "Гена Горин", "Mellstroy", "Nikita Litvinkov", "Oleg Mongol", "Gobzavr"},
				Queries: []string{"нижний интернет", "хроники нижнего интернета", "треш стримеры", "фрики интернета", "вжлинк", "литвинков", "олег монгол", "гобзавр", "пахом", "гена горин", "треш стрим"}},
			{Key: "internet.streamers", Title: "Стримеры и блогеры", About: "Популярные стримеры и блогеры: клипы стримов, ролики, интернет-драмы и скандалы.",
				Aliases: []string{"стримеры", "блогеры", "ютуберы", "интернет драмы"}, Rubric: "стримеры", Synonyms: []string{"блогеры", "стрим", "драма"},
				Format: &fmtMixed},
			{Key: "internet.trends", Title: "Тренды и челленджи", About: "Тренды TikTok и Reels, челленджи, эдиты, POV-ролики.",
				Aliases: []string{"тренды", "тикток тренды", "челленджи", "эдиты"}, Rubric: "тренды", Synonyms: []string{"челлендж", "tiktok", "эдит", "pov"}},
			{Key: "internet.ai", Title: "Нейроконтент", About: "Картинки и видео от нейросетей: нейромемы, ИИ-слоп, генерации.",
				Hint: "Новости и обзоры нейросетей — «Технологии › Нейросети».", Aliases: []string{"нейромемы", "ии слоп", "нейроарт", "ai art"},
				Rubric: "нейроконтент", Synonyms: []string{"нейромемы", "aislop", "нейроарт"}},
			{Key: "internet.creepy", Title: "Крипота", About: "Жуткое без насилия: крипипасты, лиминальные пространства, аналоговый хоррор, проклятые картинки.",
				Aliases: []string{"крипота", "крипипасты", "лиминальные пространства", "аналоговый хоррор"}, Rubric: "крипота",
				Synonyms: []string{"криповое", "лиминал", "backrooms", "analoghorror"}, Entities: []string{"Backrooms", "SCP"}},
			{Key: "internet.nostalgia", Title: "Старый интернет", About: "Ностальгия по интернету и играм 2000–2015: флеш, аська, старые мемы и сайты.",
				Aliases: []string{"ностальгия", "олдскул интернет", "нулевые"}, Rubric: "ностальгия", Synonyms: []string{"нулевые", "олдскул"}},
		}},
	{Key: "animals", Title: "Животные", Format: fmtMedia, Rubric: "животные",
		About:   "Животные на фото и видео: питомцы и дикая природа.",
		Aliases: []string{"животные", "зверушки", "милота"},
		Children: []TreeLeaf{
			{Key: "animals.cats", Title: "Кошки", About: "Кошки и котята.", Aliases: []string{"котики", "коты", "кошки"}, Rubric: "котики", Synonyms: []string{"кот", "коты", "cats"}},
			{Key: "animals.dogs", Title: "Собаки", About: "Собаки и щенки.", Aliases: []string{"собаки", "песики", "щенки"}, Rubric: "собаки", Synonyms: []string{"пёс", "щенки", "dogs"}, Entities: []string{"хаски", "корги"}},
			{Key: "animals.wild", Title: "Дикая природа", About: "Дикие звери, птицы, морские обитатели, насекомые.", Aliases: []string{"дикие животные", "птицы", "морские животные"},
				Rubric: "дикаяприрода", Synonyms: []string{"птицы", "wildlife", "звери"}},
			{Key: "animals.exotic", Title: "Экзотические питомцы", About: "Рептилии, грызуны, попугаи, хорьки и другие необычные питомцы дома.",
				Aliases: []string{"экзотические животные", "рептилии"}, Rubric: "экзотика", Synonyms: []string{"рептилии", "грызуны"}},
			{Key: "animals.farm", Title: "Ферма", About: "Домашний скот и птица: коровы, козы, лошади, куры.", Aliases: []string{"ферма", "деревня"}, Rubric: "ферма", Synonyms: []string{"деревня", "хозяйство"}},
		}},
	{Key: "people", Title: "Люди и отношения", Format: fmtMixed, Rubric: "люди",
		About:   "Жизнь людей: отношения, семья, работа, истории.",
		Aliases: []string{"люди", "отношения", "жизнь"},
		Children: []TreeLeaf{
			{Key: "people.relationships", Title: "Отношения", About: "Любовь, свидания, пары, расставания.", Aliases: []string{"любовь", "отношения", "пары"}, Rubric: "отношения", Synonyms: []string{"любовь", "свидания"}},
			{Key: "people.family", Title: "Семья и дети", About: "Родители и дети, воспитание, материнство.", Aliases: []string{"семья", "дети", "родительство", "мамы"}, Rubric: "семья", Synonyms: []string{"дети", "родители", "мамы"}},
			{Key: "people.work", Title: "Работа и офис", About: "Жизнь на работе: коллеги, начальники, офис, профессии изнутри.", Hint: "Бизнес и карьерные советы — «Бизнес».",
				Aliases: []string{"работа", "офис", "профессии"}, Rubric: "работа", Synonyms: []string{"офис", "коллеги"}},
			{Key: "people.stories", Title: "Истории из жизни", About: "Рассказы людей о случаях из жизни.", Aliases: []string{"истории", "истории из жизни"},
				Rubric: "истории", Synonyms: []string{"историяизжизни"}, Format: fmtTextLeaf},
		}},
	{Key: "games", Title: "Игры", Format: fmtMixed, Rubric: "игры",
		About:   "Видеоигры и настольные игры: геймплей, новости, мемы, обзоры.",
		Aliases: []string{"игры", "гейминг", "видеоигры", "геймеры"},
		Children: []TreeLeaf{
			{Key: "games.pc", Title: "ПК и консоли", About: "Игры на компьютере, PlayStation, Xbox, Switch.", Aliases: []string{"компьютерные игры", "консоли"}, Rubric: "игрынапк",
				Synonyms: []string{"pcgaming", "playstation", "xbox"}, Entities: []string{"Minecraft", "GTA", "Steam", "PlayStation"}},
			{Key: "games.mobile", Title: "Мобильные игры", About: "Игры на телефоне.", Aliases: []string{"мобильные игры"}, Rubric: "мобильныеигры", Synonyms: []string{"mobilegames"}, Entities: []string{"Genshin Impact", "Brawl Stars"}},
			{Key: "games.esports", Title: "Киберспорт", About: "Турниры, команды, игроки: Dota 2, CS2, Valorant.", Aliases: []string{"киберспорт", "esports"}, Rubric: "киберспорт",
				Synonyms: []string{"esports", "дота", "кс2"}, Entities: []string{"Dota 2", "CS2"}},
			{Key: "games.retro", Title: "Ретро-игры", About: "Старые игры и приставки: Dendy, Sega, PS1, игры 90-х и 2000-х.", Aliases: []string{"ретро игры", "денди"}, Rubric: "ретроигры", Synonyms: []string{"денди", "сега", "retrogaming"}},
			{Key: "games.board", Title: "Настольные игры", About: "Настолки, D&D, карточные игры.", Aliases: []string{"настолки", "настольные игры"}, Rubric: "настолки", Synonyms: []string{"boardgames", "dnd"}},
		}},
	{Key: "screen", Title: "Кино и сериалы", Format: fmtMixed, Rubric: "кино",
		About:   "Фильмы, сериалы, мультфильмы, аниме: кадры, новости, обсуждения.",
		Aliases: []string{"кино", "фильмы", "сериалы", "кино и сериалы"},
		Children: []TreeLeaf{
			{Key: "screen.movies", Title: "Фильмы", About: "Кино: кадры, трейлеры, новости, рецензии.", Aliases: []string{"фильмы", "кинчик"}, Rubric: "фильмы", Synonyms: []string{"кинофильм", "movie"}, Entities: []string{"Marvel"}},
			{Key: "screen.series", Title: "Сериалы", About: "Сериалы: кадры, новости, обсуждения сезонов.", Aliases: []string{"сериалы"}, Rubric: "сериалы", Synonyms: []string{"сериал", "netflix"}, Entities: []string{"Netflix", "HBO"}},
			{Key: "screen.anime", Title: "Аниме", About: "Аниме и манга.", Aliases: []string{"аниме", "манга", "anime"}, Rubric: "аниме", Synonyms: []string{"anime", "манга"}, Entities: []string{"Ghibli"}},
			{Key: "screen.cartoons", Title: "Мультфильмы", About: "Мультфильмы и мультсериалы.", Aliases: []string{"мультики", "мультфильмы"}, Rubric: "мультики", Synonyms: []string{"мультфильм", "cartoon"}},
			{Key: "screen.dorama", Title: "Дорамы", About: "Корейские, китайские и японские сериалы.", Aliases: []string{"дорамы", "kdrama"}, Rubric: "дорамы", Synonyms: []string{"kdrama", "дорама"}},
		}},
	{Key: "music", Title: "Музыка", Format: fmtMixed, Rubric: "музыка",
		About:   "Музыка: исполнители, клипы, концерты, новинки.",
		Aliases: []string{"музыка", "треки", "песни"},
		Children: []TreeLeaf{
			{Key: "music.artists", Title: "Исполнители и новинки", About: "Певцы, группы, рэперы, релизы и новости музыки.", Aliases: []string{"новинки музыки", "рэп", "исполнители"}, Rubric: "новинки", Synonyms: []string{"релиз", "рэп", "трек"}},
			{Key: "music.live", Title: "Клипы и концерты", About: "Видеоклипы, живые выступления, фестивали.", Aliases: []string{"клипы", "концерты"}, Rubric: "концерты", Synonyms: []string{"клип", "фестиваль", "live"}},
			{Key: "music.kpop", Title: "K-pop", About: "Корейская поп-музыка и айдолы.", Aliases: []string{"кпоп", "k-pop"}, Rubric: "кпоп", Synonyms: []string{"kpop", "bts"}},
			{Key: "music.covers", Title: "Каверы и вокал", About: "Каверы, исполнение песен, вокал, игра на инструментах.", Aliases: []string{"каверы", "вокал"}, Rubric: "каверы", Synonyms: []string{"cover", "вокал"}},
			{Key: "music.production", Title: "Битмейкинг и продакшн", About: "Создание музыки: биты, сведение, синтезаторы, студии.", Aliases: []string{"битмейкинг", "продакшн"}, Rubric: "битмейкинг",
				Synonyms: []string{"продакшн", "сведение"}, Format: &fmtText},
		}},
	{Key: "celebs", Title: "Знаменитости", Format: fmtMixed, Rubric: "звёзды",
		About:   "Известные люди: звёзды шоу-бизнеса, спорта и политики как персоны.",
		Aliases: []string{"знаменитости", "звезды", "селебрити"},
		Children: []TreeLeaf{
			{Key: "celebs.stars", Title: "Звёзды", About: "Актёры, певцы, модели: фото, выходы, новости.", Aliases: []string{"звезды", "селебы"}, Rubric: "звезды", Synonyms: []string{"селебрити", "шоубизнес"}, Entities: []string{"Илон Маск"}},
			{Key: "celebs.gossip", Title: "Сплетни и скандалы", About: "Слухи, разводы, скандалы знаменитостей.", Aliases: []string{"сплетни", "скандалы"}, Rubric: "сплетни", Synonyms: []string{"скандал", "слухи"}},
		}},
	{Key: "news", Title: "Новости", Format: fmtText, Rubric: "новости",
		About:   "Новости и события: политика, мир, страна, экономика, происшествия.",
		Aliases: []string{"новости", "новости и политика", "сми"},
		Children: []TreeLeaf{
			{Key: "news.politics", Title: "Политика", About: "Власть, выборы, законы, политики, внешняя политика.", Aliases: []string{"политика"}, Rubric: "политика", Synonyms: []string{"власть", "выборы"}},
			{Key: "news.world", Title: "Мировые новости", About: "События в других странах.", Aliases: []string{"мировые события", "в мире"}, Rubric: "вмире", Synonyms: []string{"мир", "сша", "китай"}},
			{Key: "news.russia", Title: "Новости России и городов", About: "События в России, регионах и городах.", Aliases: []string{"новости россии", "новости города", "местные новости"},
				Rubric: "россия", Synonyms: []string{"новостироссии", "город"}},
			{Key: "news.economy", Title: "Экономика", About: "Цены, курсы, рынки, компании, экономическая политика.", Aliases: []string{"экономика"}, Rubric: "экономика", Synonyms: []string{"курс", "цены"}},
			{Key: "news.crime", Title: "Криминал и происшествия", About: "Преступления, суды, расследования, ЧП, тру-крайм.", Aliases: []string{"криминал", "тру крайм", "происшествия"}, Rubric: "криминал", Synonyms: []string{"трукрайм", "суд", "чп"}},
			{Key: "news.military", Title: "Армия и военная техника", About: "Вооружённые силы, техника, оружие, военная история.", Aliases: []string{"армия", "военная техника", "оружие"}, Rubric: "армия", Synonyms: []string{"военные", "оружие"}},
		}},
	{Key: "tech", Title: "Технологии", Format: fmtText, Rubric: "технологии",
		About:   "Техника и IT: гаджеты, нейросети, программирование, софт, безопасность.",
		Aliases: []string{"технологии", "it", "айти", "техника"},
		Children: []TreeLeaf{
			{Key: "tech.gadgets", Title: "Гаджеты", About: "Смартфоны, ноутбуки, умные устройства: новинки и обзоры.", Aliases: []string{"гаджеты", "смартфоны"}, Rubric: "гаджеты", Synonyms: []string{"смартфон", "айфон"}, Entities: []string{"Apple", "iPhone"}},
			{Key: "tech.ai", Title: "Нейросети", About: "Новости, инструменты и практика ИИ: ChatGPT, Midjourney, локальные модели.", Hint: "Сами картинки от нейросетей — «Нейроконтент».",
				Aliases: []string{"нейросети", "ии", "искусственный интеллект"}, Rubric: "нейросети", Synonyms: []string{"ии", "ai", "chatgpt"}, Entities: []string{"ChatGPT", "Midjourney", "Claude"}},
			{Key: "tech.code", Title: "Программирование", About: "Разработка, языки, фреймворки, жизнь программистов.", Aliases: []string{"программирование", "разработка", "кодинг"}, Rubric: "программирование",
				Synonyms: []string{"кодинг", "dev", "python"}, Entities: []string{"Python"}},
			{Key: "tech.software", Title: "Приложения и сервисы", About: "Программы, приложения, онлайн-сервисы, лайфхаки по ним.", Aliases: []string{"софт", "приложения"}, Rubric: "софт", Synonyms: []string{"приложения", "сервисы"}},
			{Key: "tech.security", Title: "Кибербезопасность", About: "Взломы, утечки, защита данных, хакеры.", Aliases: []string{"кибербезопасность", "хакеры"}, Rubric: "кибербез", Synonyms: []string{"хакеры", "утечки"}},
			{Key: "tech.hardware", Title: "Железо и сборки ПК", About: "Комплектующие, видеокарты, сборки компьютеров.", Aliases: []string{"железо", "сборка пк"}, Rubric: "железо", Synonyms: []string{"сборкапк", "видеокарты"}},
		}},
	{Key: "business", Title: "Бизнес", Format: fmtMixed, Rubric: "бизнес",
		About:   "Предпринимательство, маркетинг, продажи, карьера и право.",
		Aliases: []string{"бизнес", "бизнес и маркетинг"},
		Children: []TreeLeaf{
			{Key: "business.owners", Title: "Предпринимательство", About: "Свой бизнес: открытие, управление, кейсы предпринимателей в любой отрасли.", Aliases: []string{"свой бизнес", "предприниматели"},
				Rubric: "предприниматель", Synonyms: []string{"свойбизнес", "бизнескейс"}},
			{Key: "business.marketing", Title: "Маркетинг и реклама", About: "Маркетинг, реклама, бренды, продвижение.", Aliases: []string{"маркетинг", "реклама"}, Rubric: "маркетинг", Synonyms: []string{"реклама", "брендинг"}},
			{Key: "business.smm", Title: "SMM и блогинг", About: "Ведение соцсетей и каналов, контент-маркетинг, рост аудитории.", Aliases: []string{"смм", "smm", "блогинг"}, Rubric: "смм", Synonyms: []string{"smm", "блогинг"}},
			{Key: "business.ecom", Title: "Маркетплейсы", About: "Продажи на Wildberries, Ozon и в интернет-магазинах.", Aliases: []string{"маркетплейсы", "wildberries", "ozon"}, Rubric: "маркетплейсы",
				Synonyms: []string{"wildberries", "ozon", "ecom"}, Entities: []string{"Wildberries", "Ozon"}},
			{Key: "business.startups", Title: "Стартапы и франшизы", About: "Стартапы, инвестиции в проекты, франшизы.", Aliases: []string{"стартапы", "франшизы"}, Rubric: "стартапы", Synonyms: []string{"стартап", "франшиза"}},
			{Key: "business.freelance", Title: "Фриланс и удалёнка", About: "Работа на себя, удалённая работа, заказы.", Aliases: []string{"фриланс", "удаленка"}, Rubric: "фриланс", Synonyms: []string{"удаленка", "remote"}},
			{Key: "business.sales", Title: "Продажи и переговоры", About: "Техники продаж, переговоры, работа с клиентами.", Aliases: []string{"продажи"}, Rubric: "продажи", Synonyms: []string{"переговоры", "sales"}},
			{Key: "business.career", Title: "Карьера и HR", About: "Поиск работы, резюме, собеседования, найм.", Aliases: []string{"карьера", "hr", "работа и карьера"}, Rubric: "карьера", Synonyms: []string{"hr", "резюме"}},
			{Key: "business.legal", Title: "Право", About: "Законы, права, юридические советы.", Aliases: []string{"право", "юристы", "юридические советы"}, Rubric: "право", Synonyms: []string{"юрист", "закон"}},
		}},
	{Key: "finance", Title: "Деньги и инвестиции", Format: fmtMixed, Rubric: "деньги",
		About:   "Личные деньги: инвестиции, крипта, налоги, финансовая грамотность.",
		Aliases: []string{"деньги", "финансы"},
		Children: []TreeLeaf{
			{Key: "finance.crypto", Title: "Криптовалюта", About: "Биткоин, альткоины, блокчейн, трейдинг крипты.", Aliases: []string{"крипта", "криптовалюты", "биткоин"}, Rubric: "крипта",
				Synonyms: []string{"биткоин", "crypto", "блокчейн"}, Entities: []string{"Bitcoin"}},
			{Key: "finance.invest", Title: "Инвестиции и трейдинг", About: "Акции, облигации, фонды, трейдинг.", Aliases: []string{"инвестиции", "трейдинг", "акции"}, Rubric: "инвестиции", Synonyms: []string{"акции", "трейдинг"}},
			{Key: "finance.personal", Title: "Личные финансы и налоги", About: "Бюджет, накопления, кредиты, налоги, банки.", Aliases: []string{"личные финансы", "налоги", "финансовая грамотность"}, Rubric: "личныефинансы",
				Synonyms: []string{"налоги", "бюджет", "кредиты"}},
		}},
	{Key: "home", Title: "Дом и сад", Format: fmtMedia, Rubric: "дом",
		About:   "Жильё и всё вокруг него: недвижимость, ремонт, интерьер, сад.",
		Aliases: []string{"дом", "дом и сад", "уют"},
		Children: []TreeLeaf{
			{Key: "home.realty", Title: "Недвижимость", About: "Покупка, аренда, ипотека, новостройки, цены на жильё.", Aliases: []string{"недвижимость", "квартиры", "ипотека"}, Rubric: "недвижимость",
				Synonyms: []string{"ипотека", "квартира"}, Format: fmtTextLeaf},
			{Key: "home.renovation", Title: "Ремонт и стройка", About: "Ремонт квартир, строительство домов, материалы, бригады.", Aliases: []string{"ремонт", "строительство"}, Rubric: "ремонт",
				Synonyms: []string{"стройка", "строительство"}, Format: fmtTextLeaf},
			{Key: "home.interior", Title: "Интерьер", About: "Дизайн интерьера, обустройство и уют.", Aliases: []string{"интерьер", "дизайн интерьера"}, Rubric: "интерьер", Synonyms: []string{"дизайнинтерьера", "уют"}, Format: fmtImageLf},
			{Key: "home.garden", Title: "Сад и огород", About: "Дача, огород, сад, урожай.", Aliases: []string{"дача", "огород", "сад"}, Rubric: "дача", Synonyms: []string{"огород", "сад"}, Format: fmtImageLf},
			{Key: "home.plants", Title: "Комнатные растения", About: "Цветы и растения в доме, уход за ними.", Aliases: []string{"комнатные растения", "цветы"}, Rubric: "растения", Synonyms: []string{"цветы", "plants"}, Format: fmtImageLf},
		}},
	{Key: "self", Title: "Психология и саморазвитие", Format: fmtText, Rubric: "саморазвитие",
		About:   "Внутренний мир: психология, мотивация, эзотерика.",
		Aliases: []string{"психология", "саморазвитие"},
		Children: []TreeLeaf{
			{Key: "self.psychology", Title: "Психология", About: "Психология, отношения с собой, ментальное здоровье, коучинг.", Aliases: []string{"психология", "коучинг"}, Rubric: "психология", Synonyms: []string{"психолог", "коучинг"}},
			{Key: "self.motivation", Title: "Мотивация и цитаты", About: "Цитаты, афоризмы, мотивирующие посты.", Aliases: []string{"цитаты", "мотивация", "афоризмы"}, Rubric: "цитаты",
				Synonyms: []string{"мотивация", "афоризмы"}, Format: &TopicFormat{Media: "optional", Text: "optional", Judge: "both"}},
			{Key: "self.esoteric", Title: "Эзотерика и астрология", About: "Гороскопы, таро, нумерология, магия.", Aliases: []string{"эзотерика", "астрология", "гороскопы", "таро"}, Rubric: "эзотерика", Synonyms: []string{"гороскоп", "таро", "астрология"}},
		}},
	{Key: "science", Title: "Наука и образование", Format: fmtText, Rubric: "наука",
		About:   "Знания: наука, история, факты, учёба, языки.",
		Aliases: []string{"наука", "наука и знания", "образование", "познавательное"},
		Children: []TreeLeaf{
			{Key: "science.space", Title: "Космос", About: "Космос, астрономия, ракеты, снимки планет.", Aliases: []string{"космос", "астрономия"}, Rubric: "космос", Synonyms: []string{"астрономия", "nasa"}, Entities: []string{"NASA", "SpaceX"}},
			{Key: "science.history", Title: "История", About: "История стран и людей, архивные фото.", Aliases: []string{"история", "исторические фото"}, Rubric: "история", Synonyms: []string{"ссср", "архив"}},
			{Key: "science.facts", Title: "Интересные факты", About: "Познавательные факты обо всём.", Aliases: []string{"факты", "интересные факты"}, Rubric: "факты", Synonyms: []string{"интересно", "знаетели"}},
			{Key: "science.physics", Title: "Физика и математика", About: "Физика, математика, научные открытия и опыты.", Aliases: []string{"физика", "математика"}, Rubric: "физика", Synonyms: []string{"математика", "опыты"}},
			{Key: "science.engineering", Title: "Инженерия и изобретения", About: "Техника, механизмы, изобретения, как это устроено.", Aliases: []string{"инженерия", "изобретения"}, Rubric: "инженерия", Synonyms: []string{"изобретения", "механизмы"}},
			{Key: "science.education", Title: "Учёба", About: "Школа, вузы, экзамены, полезные материалы для учёбы.", Aliases: []string{"учеба", "образование", "егэ"}, Rubric: "учеба", Synonyms: []string{"егэ", "студенты"}},
			{Key: "science.languages", Title: "Иностранные языки", About: "Изучение английского и других языков.", Aliases: []string{"английский", "иностранные языки"}, Rubric: "языки", Synonyms: []string{"английский", "english"}},
		}},
	{Key: "culture", Title: "Искусство и книги", Format: fmtMixed, Rubric: "искусство",
		About:   "Культура: книги, стихи, живопись и арт, театр, религия.",
		Aliases: []string{"культура", "искусство", "культура и книги"},
		Children: []TreeLeaf{
			{Key: "culture.books", Title: "Книги", About: "Книги, цитаты из книг, рекомендации, писатели.", Aliases: []string{"книги", "литература"}, Rubric: "книги", Synonyms: []string{"литература", "чтение"}},
			{Key: "culture.poetry", Title: "Стихи", About: "Поэзия, стихотворения, поэты.", Aliases: []string{"стихи", "поэзия"}, Rubric: "стихи", Synonyms: []string{"поэзия"}, Format: fmtTextLeaf},
			{Key: "culture.art", Title: "Живопись и арт", About: "Картины, рисунки, цифровой арт, иллюстрации.", Aliases: []string{"живопись", "арт", "рисунки", "искусство"}, Rubric: "арт",
				Synonyms: []string{"живопись", "рисунок", "art"}, Format: fmtImageLf},
			{Key: "culture.theatre", Title: "Театр и балет", About: "Театр, балет, опера.", Aliases: []string{"театр", "балет"}, Rubric: "театр", Synonyms: []string{"балет", "опера"}},
			{Key: "culture.religion", Title: "Религия", About: "Вера, храмы, праздники, религиозные тексты.", Aliases: []string{"религия", "вера"}, Rubric: "религия", Synonyms: []string{"вера", "храм"}},
		}},
	{Key: "food", Title: "Еда", Format: fmtMixed, Rubric: "еда",
		About:   "Еда и напитки: рецепты, блюда, рестораны.",
		Aliases: []string{"еда", "кулинария", "вкусно"},
		Children: []TreeLeaf{
			{Key: "food.recipes", Title: "Рецепты", About: "Рецепты блюд с ингредиентами или пошаговым приготовлением.", Aliases: []string{"рецепты"}, Rubric: "рецепты", Synonyms: []string{"рецепт", "готовим"}},
			{Key: "food.baking", Title: "Выпечка и десерты", About: "Торты, пироги, хлеб, сладости.", Aliases: []string{"выпечка", "десерты"}, Rubric: "выпечка", Synonyms: []string{"десерты", "торт"}},
			{Key: "food.places", Title: "Рестораны и стритфуд", About: "Блюда в заведениях, обзоры ресторанов, уличная еда.", Aliases: []string{"рестораны", "стритфуд"}, Rubric: "рестораны", Synonyms: []string{"стритфуд", "кафе"}},
			{Key: "food.drinks", Title: "Напитки", About: "Кофе, чай, коктейли, вино, пиво.", Aliases: []string{"напитки", "коктейли", "кофе"}, Rubric: "напитки", Synonyms: []string{"кофе", "коктейли"}},
		}},
	{Key: "travel", Title: "Путешествия", Format: fmtMixed, Rubric: "путешествия",
		About:   "Места и поездки: природа, города, походы, заброшки.",
		Aliases: []string{"путешествия", "места", "места и путешествия", "туризм"},
		Children: []TreeLeaf{
			{Key: "travel.nature", Title: "Природа и пейзажи", About: "Красивые виды: горы, леса, моря, закаты.", Aliases: []string{"природа", "пейзажи"}, Rubric: "природа", Synonyms: []string{"пейзаж", "nature"}, Format: fmtImageLf},
			{Key: "travel.cities", Title: "Города и архитектура", About: "Жизнь городов и регионов: местные новости и события, городская среда, люди, места, улицы, здания и архитектура.", Aliases: []string{"города", "архитектура"}, Rubric: "города", Synonyms: []string{"архитектура", "city"}},
			{Key: "travel.russia", Title: "Путешествия по России", About: "Места и поездки по России.", Aliases: []string{"путешествия по россии"}, Rubric: "попутешествиипороссии", Synonyms: []string{"россия", "поездки"}},
			{Key: "travel.hiking", Title: "Походы и кемпинг", About: "Походы, палатки, горы, снаряжение.", Aliases: []string{"походы", "кемпинг"}, Rubric: "походы", Synonyms: []string{"кемпинг", "hiking"}},
			{Key: "travel.abandoned", Title: "Заброшки", About: "Заброшенные здания и места, сталкинг.", Aliases: []string{"заброшки", "заброшенные места"}, Rubric: "заброшки", Synonyms: []string{"заброшенное", "урбекс"}},
			{Key: "travel.tips", Title: "Советы путешественникам", About: "Билеты, визы, лайфхаки, куда поехать.", Aliases: []string{"советы туристам"}, Rubric: "лайфхакипутешествий", Synonyms: []string{"визы", "билеты"}, Format: fmtTextLeaf},
		}},
	{Key: "style", Title: "Мода и красота", Format: fmtMixed, Rubric: "стиль",
		About:   "Внешность: одежда, макияж, уход, тату.",
		Aliases: []string{"мода", "красота", "внешность и стиль", "стиль"},
		Children: []TreeLeaf{
			{Key: "style.fashion", Title: "Мода", About: "Одежда, образы, тренды, бренды.", Aliases: []string{"мода", "одежда"}, Rubric: "мода", Synonyms: []string{"образ", "fashion"}},
			{Key: "style.beauty", Title: "Макияж и уход", About: "Макияж, уход за кожей и волосами, маникюр.", Aliases: []string{"макияж", "уход", "бьюти"}, Rubric: "бьюти", Synonyms: []string{"макияж", "уход"}},
			{Key: "style.tattoo", Title: "Тату и пирсинг", About: "Татуировки и пирсинг.", Aliases: []string{"тату", "татуировки"}, Rubric: "тату", Synonyms: []string{"tattoo", "пирсинг"}},
			{Key: "style.mens", Title: "Мужской стиль", About: "Мужская одежда, стрижки, бороды, аксессуары.", Aliases: []string{"мужской стиль"}, Rubric: "мужскойстиль", Synonyms: []string{"барбер", "борода"}},
		}},
	{Key: "health", Title: "Здоровье и фитнес", Format: fmtMixed, Rubric: "здоровье",
		About:   "Тело и здоровье: медицина, тренировки, питание.",
		Aliases: []string{"здоровье", "зож", "фитнес"},
		Children: []TreeLeaf{
			{Key: "health.medicine", Title: "Медицина", About: "Болезни, лечение, врачи, советы о здоровье.", Aliases: []string{"медицина", "здоровье"}, Rubric: "медицина", Synonyms: []string{"врач", "лечение"}, Format: fmtTextLeaf},
			{Key: "health.fitness", Title: "Фитнес", About: "Тренировки, спортзал, похудение, упражнения.", Aliases: []string{"фитнес", "спортзал", "тренировки"}, Rubric: "фитнес", Synonyms: []string{"тренировка", "качалка"}},
			{Key: "health.nutrition", Title: "Правильное питание", About: "ПП, диеты, БЖУ, полезные рецепты.", Aliases: []string{"пп", "правильное питание", "здоровое питание"}, Rubric: "пп", Synonyms: []string{"диета", "зож"}},
		}},
	{Key: "transport", Title: "Транспорт", Format: fmtMixed, Rubric: "транспорт",
		About:   "Машины, мотоциклы, самолёты, поезда.",
		Aliases: []string{"транспорт", "техника"},
		Children: []TreeLeaf{
			{Key: "transport.cars", Title: "Автомобили", About: "Машины: новинки, тюнинг, ретро, дороги, автоновости.", Aliases: []string{"авто", "машины", "автомобили", "тачки"}, Rubric: "авто", Synonyms: []string{"машины", "тачки", "cars"}},
			{Key: "transport.moto", Title: "Мотоциклы", About: "Мотоциклы и мотоциклисты.", Aliases: []string{"мото", "мотоциклы"}, Rubric: "мото", Synonyms: []string{"мотоцикл", "байкеры"}, Entities: []string{"Harley-Davidson"}},
			{Key: "transport.aviation", Title: "Авиация", About: "Самолёты, вертолёты, аэропорты.", Aliases: []string{"авиация", "самолеты"}, Rubric: "авиация", Synonyms: []string{"самолёты", "aviation"}},
			{Key: "transport.rail", Title: "Поезда и метро", About: "Железная дорога, поезда, метро, трамваи.", Aliases: []string{"поезда", "метро", "жд"}, Rubric: "поезда", Synonyms: []string{"метро", "жд"}},
		}},
	{Key: "hobby", Title: "Хобби", Format: fmtMedia, Rubric: "хобби",
		About:   "Увлечения: рукоделие, своими руками, фото, косплей, рыбалка.",
		Aliases: []string{"хобби", "увлечения", "хобби и творчество"},
		Children: []TreeLeaf{
			{Key: "hobby.diy", Title: "Своими руками", About: "Самоделки, мастерская, поделки, ремонт вещей.", Aliases: []string{"своими руками", "diy", "самоделки"}, Rubric: "своимируками", Synonyms: []string{"diy", "самоделки"}},
			{Key: "hobby.crafts", Title: "Рукоделие", About: "Вязание, шитьё, вышивка, бисер.", Aliases: []string{"рукоделие", "вязание"}, Rubric: "рукоделие", Synonyms: []string{"вязание", "шитьё"}},
			{Key: "hobby.photo", Title: "Фотография", About: "Фотография как хобби: снимки, техника, советы.", Aliases: []string{"фотография", "фото"}, Rubric: "фотография", Synonyms: []string{"фотограф", "photo"}},
			{Key: "hobby.cosplay", Title: "Косплей", About: "Костюмы персонажей и косплееры.", Aliases: []string{"косплей", "cosplay"}, Rubric: "косплей", Synonyms: []string{"cosplay"}},
			{Key: "hobby.fishing", Title: "Рыбалка и охота", About: "Рыбалка, охота, снаряжение, трофеи.", Aliases: []string{"рыбалка", "охота"}, Rubric: "рыбалка", Synonyms: []string{"охота", "улов"}},
			{Key: "hobby.collecting", Title: "Коллекционирование", About: "Коллекции: монеты, фигурки, модели, винтаж.", Aliases: []string{"коллекционирование", "коллекции"}, Rubric: "коллекции", Synonyms: []string{"нумизматика", "фигурки"}},
		}},
	{Key: "sport", Title: "Спорт", Format: fmtMixed, Rubric: "спорт",
		About:   "Спорт: матчи, моменты, новости, спортсмены.",
		Aliases: []string{"спорт"},
		Children: []TreeLeaf{
			{Key: "sport.football", Title: "Футбол", About: "Футбол: матчи, голы, клубы, игроки.", Aliases: []string{"футбол"}, Rubric: "футбол", Synonyms: []string{"football", "рпл"}},
			{Key: "sport.fight", Title: "Единоборства", About: "ММА, бокс, борьба, бои.", Aliases: []string{"единоборства", "мма", "бокс"}, Rubric: "мма", Synonyms: []string{"бокс", "ufc"}, Entities: []string{"UFC"}},
			{Key: "sport.hockey", Title: "Хоккей", About: "Хоккей: КХЛ, НХЛ.", Aliases: []string{"хоккей"}, Rubric: "хоккей", Synonyms: []string{"кхл", "нхл"}},
			{Key: "sport.basketball", Title: "Баскетбол", About: "Баскетбол: NBA, Единая лига.", Aliases: []string{"баскетбол"}, Rubric: "баскетбол", Synonyms: []string{"nba"}},
			{Key: "sport.motorsport", Title: "Автоспорт", About: "Формула-1, ралли, гонки.", Aliases: []string{"автоспорт", "формула 1"}, Rubric: "автоспорт", Synonyms: []string{"f1", "гонки"}},
			{Key: "sport.extreme", Title: "Экстрим", About: "Паркур, скейт, сноуборд, трюки.", Aliases: []string{"экстрим", "экстремальный спорт"}, Rubric: "экстрим", Synonyms: []string{"трюки", "паркур"}},
		}},
}

// LegacyTagKeys — старые ключи тем (до редакции 2026-10-29) → новые. Ключ ветки вместо листа — «общая тема ветки».
var LegacyTagKeys = map[string]string{
	"adult.other": "adult", "shock.creepy": "internet.creepy", "shock.crashes": "shock.accidents", "shock.other": "shock",
	"humor.relatable": "memes.relatable", "humor.absurd": "memes.absurd", "humor.wholesome": "memes.wholesome", "memes.other": "memes",
	"humor.dark": "memes.dark", "humor.fails": "humor.videos", "humor.funny_videos": "humor.videos", "humor.sketches": "humor.standup", "humor.other": "humor",
	"internet.brainrot": "memes.absurd", "internet.gen_alpha": "memes.absurd", "internet.ai_slop": "internet.ai", "internet.cursed": "internet.creepy",
	"internet.liminal": "internet.creepy", "internet.analog_horror": "internet.creepy", "internet.characters": "memes", "internet.copypasta": "humor.jokes",
	"internet.edits": "internet.trends", "internet.pov": "internet.trends", "internet.tiktok_trends": "internet.trends", "internet.subcultures": "memes.relatable",
	"internet.drama": "internet.streamers", "internet.other": "internet",
	"animals.birds": "animals.wild", "animals.sea": "animals.wild", "animals.other": "animals",
	"people.couples": "people.relationships", "people.life": "people", "people.men": "people", "people.women": "people", "people.other": "people",
	"games.other": "games", "screen.reviews": "screen", "screen.other": "screen",
	"music.clips": "music.live", "music.other": "music", "celebs.bloggers": "internet.streamers", "celebs.other": "celebs",
	"news.local": "news.russia", "news.other": "news", "tech.other": "tech",
	"money.business": "business.owners", "money.marketing": "business.marketing", "money.smm": "business.smm", "money.ecom": "business.ecom",
	"money.legal": "business.legal", "money.hr": "business.career", "money.food": "food.places", "money.beauty": "style.beauty", "money.auto": "transport.cars",
	"money.tourism": "travel", "money.franchise": "business.startups", "money.freelance": "business.freelance", "money.sales": "business.sales", "money.other": "business",
	"money.crypto": "finance.crypto", "money.invest": "finance.invest", "money.finance": "finance.personal", "finance.other": "finance",
	"expert.psych": "self.psychology", "expert.education": "science.education", "expert.esoteric": "self.esoteric", "expert.parenting": "people.family",
	"expert.languages": "science.languages", "expert.other": "self", "expert.medicine": "health.medicine",
	"culture.books": "culture.books", "culture.poetry": "culture.poetry", "culture.painting": "culture.art", "culture.other": "culture",
	"science.other": "science", "food.dishes": "food.places", "food.other": "food", "food.healthy": "health.nutrition",
	"travel.other": "travel", "style.fitness": "health.fitness", "style.other": "style", "health.other": "health",
	"cars.cars": "transport.cars", "cars.moto": "transport.moto", "cars.aviation": "transport.aviation", "cars.rail": "transport.rail", "cars.other": "transport",
	"money.realty": "home.realty", "money.construction": "home.renovation", "money.interior": "home.interior", "hobby.garden": "home.garden", "hobby.plants": "home.plants", "home.other": "home",
	"hobby.art": "culture.art", "hobby.quotes": "self.motivation", "hobby.other": "hobby", "sport.other": "sport",
	"other.misc": "", "other.other": "",
	// ветки, сменившие ключ
	"money": "business", "expert": "self", "cars": "transport", "other": "",
}

// FormatHashtags — хэштеги формы поста: подходят к любой теме.
var FormatHashtags = []struct {
	Tag      string
	Synonyms []string
}{
	{"видео", []string{"ролик", "видос"}},
	{"комикс", []string{"комиксы", "comic"}},
	{"переписка", []string{"скрин", "скриншот", "чат"}},
	{"подборка", []string{"сборник", "топ"}},
	{"статья", []string{"лонгрид"}},
	{"гайд", []string{"инструкция", "туториал", "лайфхак"}},
	{"обзор", []string{"распаковка", "review"}},
	{"инфографика", []string{}},
}
