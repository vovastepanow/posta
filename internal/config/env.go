package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Server — параметры сервера: из переменных окружения, а если их нет — из файла posta.env рядом с бинарником
// или в рабочей папке (KEY=VALUE, # — комментарий). Секреты (ключи API) — только здесь, не в коде и не в git.
type Server struct {
	Root        string // папка проекта: web/, data/
	Listen      string // адрес HTTP, по умолчанию 127.0.0.1:8090
	BasePath    string // префикс за обратным прокси, например /posta
	PostgresDSN string
	ClickHouse  string // адрес ClickHouse (tcp), пусто — без аналитики
	CHDatabase  string
	MediaDir    string // кэш медиа (было MEDIA_CACHE_DIR)
	MediaURL    string // как медиа видно браузеру (относительно BasePath)
	DataDir     string // data/: выгрузки, очередь файлов, кэши yt-dlp
	SecretKeyFile string
	PublicURL   string // https-адрес сайта: возвраты из OAuth соцсетей и VK

	OllamaURL       string
	OpenRouterKey   string
	OpenRouterURL   string
	OpenRouterTitle string // заголовок X-Title для статистики OpenRouter

	VKClientID      string
	VKRedirectURI   string
	VKServiceToken  string
	VKOAuthScope    string
	NetworkKeys     map[string]string // REDDIT_CLIENT_ID, X_CLIENT_SECRET… — ключи приложений соцсетей

	FFmpeg  string
	FFprobe string
	YtDlp   string
	Chrome  string

	Dev bool // режим разработки: подробные ошибки, без запуска сборщиков из вкладок (POSTA_DEV=1)
}

func LoadServer() Server {
	root := env("POSTA_ROOT", "")
	if root == "" {
		root, _ = os.Getwd()
	}
	loadEnvFile(filepath.Join(root, "posta.env"))
	s := Server{
		Root:          root,
		Listen:        env("POSTA_LISTEN", "127.0.0.1:8090"),
		BasePath:      strings.TrimRight(env("POSTA_BASE_PATH", ""), "/"),
		PostgresDSN:   env("POSTA_PG", "postgres://posta@127.0.0.1:5432/posta?sslmode=disable"),
		ClickHouse:    env("POSTA_CH", ""),
		CHDatabase:    env("POSTA_CH_DB", "posta"),
		MediaDir:      env("POSTA_MEDIA_DIR", filepath.Join(root, "media_cache")),
		MediaURL:      env("POSTA_MEDIA_URL", "media_cache"),
		DataDir:       env("POSTA_DATA_DIR", filepath.Join(root, "data")),
		SecretKeyFile: env("POSTA_SECRET_KEY_FILE", filepath.Join(root, "secret.key")),
		PublicURL:     strings.TrimRight(env("POSTA_PUBLIC_URL", ""), "/"),
		OllamaURL:     strings.TrimRight(env("OLLAMA_URL", "http://localhost:11434"), "/"),
		OpenRouterKey: env("OPENROUTER_API_KEY", ""),
		OpenRouterURL: strings.TrimRight(env("OPENROUTER_URL", "https://openrouter.ai/api/v1"), "/"),
		OpenRouterTitle: env("OPENROUTER_TITLE", "Posta"),
		VKClientID:    env("VK_CLIENT_ID", ""),
		VKRedirectURI: env("VK_REDIRECT_URI", ""),
		VKServiceToken: env("VK_SERVICE_TOKEN", ""),
		VKOAuthScope:  env("VK_OAUTH_SCOPE", ""),
		FFmpeg:        env("FFMPEG", "ffmpeg"),
		FFprobe:       env("FFPROBE", "ffprobe"),
		YtDlp:         env("YTDLP", filepath.Join(root, "tools/bin/yt-dlp")),
		Chrome:        env("CHROME", "google-chrome"),
		Dev:           env("POSTA_DEV", "") == "1",
		NetworkKeys:   map[string]string{},
	}
	for _, name := range []string{"REDDIT", "TUMBLR", "LINKEDIN", "PINTEREST", "YOUTUBE", "X"} {
		for _, part := range []string{"_CLIENT_ID", "_CLIENT_SECRET"} {
			if v := env(name+part, ""); v != "" {
				s.NetworkKeys[name+part] = v
			}
		}
	}
	if v := env("OK_APP_KEY", ""); v != "" {
		s.NetworkKeys["OK_APP_KEY"] = v
	}
	return s
}

// URL сайта для ссылок наружу (возврат из OAuth): POSTA_PUBLIC_URL или пусто.
func (s Server) Abs(path string) string { return s.PublicURL + "/" + strings.TrimLeft(path, "/") }

func env(name, def string) string {
	if v, ok := os.LookupEnv(name); ok && v != "" {
		return v
	}
	return def
}

// EnvInt — целое из окружения (для тонкой настройки без пересборки).
func EnvInt(name string, def int) int {
	if v, err := strconv.Atoi(env(name, "")); err == nil {
		return v
	}
	return def
}

// loadEnvFile дописывает в окружение переменные из файла; уже заданные в окружении не перекрываются.
func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, val)
		}
	}
}
