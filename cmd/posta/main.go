// posta — сервер и служебные команды приложения Posta.
//
//	posta serve                       — сайт и API, публикатор, сборщики, радар (systemd: posta.service)
//	posta migrate                     — схема Postgres и ClickHouse
//	posta import-sqlite <файл> <id>   — перенос из прежней базы SQLite: профиль владельца и общие данные
//	posta <команда> …                 — служебные команды (posta help)
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/importer"
	"posta/internal/jx"
	"posta/internal/llm"
	"posta/internal/posta"
	"posta/internal/web"
)

const usage = `posta — сервер и служебные команды Posta

  serve                              сайт и API (адрес — POSTA_LISTEN), публикатор, сборщики, радар
  migrate                            применить миграции Postgres и ClickHouse
  import-sqlite <файл> <id владельца> перенести данные из прежней базы SQLite в пустой Postgres
  seed-topics                        засеять дерево тем (повтор ничего не меняет)
  user-password <id|логин> [логин]   новый пароль профиля (печатается); с логином — заодно задать логин
  owner <id> [--remove]              владелец: админка, радар, бета-соцсети (без id — список)
  login-bot <токен> <имя бота>       бот входа через Telegram
  login-bot-from-channel <id>        бот входа — токен бота первого канала профиля
  adult-allow <id> on|off            18+ для профиля
  balance-add <id> <₽> [заметка]     пополнить баланс
  ua-filter on|off                   фильтр украинского контента
  discovery-run <id> [тема]          прогон автоподбора источников: по теме профиля или по указанной
  search-test <запрос> [тема]        проверка поиска по площадкам без записи в пул
  discovery-origins [тема]           откуда берутся предложения источников и как их решают
  leads-scan [id]                    проход радара лидов
  outbox-repair [--dry-run] [id]     починить очередь постов
  media-repair <id> [--dry-run]      исправить ссылки на кэш медиа
  users-gc                           удалить пустые анонимные профили
  users-merge <из> <в>               слить профили
  llm-preset free|local|hybrid|cloud выбрать набор моделей
`

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	args := os.Args[1:]
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return
	}
	cfg := config.LoadServer()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var err error
	switch args[0] {
	case "serve":
		err = serve(ctx, cfg)
	case "migrate":
		err = migrate(ctx, cfg)
	case "import-sqlite":
		err = importSQLite(ctx, cfg, args[1:])
	default:
		err = command(ctx, cfg, args)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "ошибка:", err)
		os.Exit(1)
	}
}

func migrate(ctx context.Context, cfg config.Server) error {
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer d.Close()
	applied, err := d.Migrate(ctx)
	if err != nil {
		return err
	}
	fmt.Println("Postgres: применено миграций:", len(applied), strings.Join(applied, ", "))
	if cfg.ClickHouse != "" {
		sink, err := ch.Open(ctx, cfg.ClickHouse, cfg.CHDatabase)
		if err != nil {
			return fmt.Errorf("ClickHouse: %w", err)
		}
		defer sink.Close()
		if err := sink.Migrate(ctx); err != nil {
			return fmt.Errorf("ClickHouse: %w", err)
		}
		fmt.Println("ClickHouse: схема готова")
	}
	return nil
}

func importSQLite(ctx context.Context, cfg config.Server, args []string) error {
	if len(args) < 2 {
		return errors.New("posta import-sqlite <файл .sqlite> <id владельца>")
	}
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return err
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		return err
	}
	r, err := importer.Import(ctx, d, args[0], args[1])
	if err != nil {
		return err
	}
	for _, l := range r.Lines {
		fmt.Println(l)
	}
	return nil
}

func serve(ctx context.Context, cfg config.Server) error {
	a, err := posta.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	if res, err := a.TopicTreeSeed(ctx, nil); err != nil {
		log.Printf("дерево тем: %v", err)
	} else if res["added"]+res["updated"] > 0 {
		log.Printf("дерево тем: %v", res)
	}
	a.StartBackground()
	srv := &http.Server{Addr: cfg.Listen, Handler: web.New(a).Handler(), ReadHeaderTimeout: 15 * time.Second, IdleTimeout: 2 * time.Minute}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("Posta слушает %s", cfg.Listen)
	select {
	case <-ctx.Done():
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shut, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return srv.Shutdown(shut)
}

func need(args []string, n int, hint string) error {
	if len(args) < n {
		return errors.New("использование: posta " + hint)
	}
	return nil
}

func flag(args []string, name string) bool {
	for _, a := range args {
		if a == name {
			return true
		}
	}
	return false
}

func command(ctx context.Context, cfg config.Server, args []string) error {
	a, err := posta.New(ctx, cfg)
	if err != nil {
		return err
	}
	defer a.Close()
	print := func(line string) { fmt.Println(line) }
	switch args[0] {
	case "seed-topics":
		r, err := a.TopicTreeSeed(ctx, print)
		fmt.Println(r)
		return err
	case "user-password":
		if err := need(args, 2, "user-password <id|логин> [логин]"); err != nil {
			return err
		}
		uid := args[1]
		if !posta.IsValidUserID(uid) {
			login, err := posta.CredentialsLoginNormalize(uid)
			if err != nil {
				return err
			}
			if err := a.DB.Pool.QueryRow(ctx, `SELECT id FROM users WHERE login = $1`, login).Scan(&uid); err != nil {
				return errors.New("нет профиля с таким логином")
			}
		}
		login := ""
		if len(args) > 2 {
			login = args[2]
		} else if err := a.DB.Pool.QueryRow(ctx, `SELECT COALESCE(login, '') FROM users WHERE id = $1`, uid).Scan(&login); err != nil || login == "" {
			return errors.New("у профиля нет логина: укажите его вторым аргументом")
		}
		password := posta.PKCEVerifier()[:16]
		a.DB.Pool.Exec(ctx, `UPDATE users SET password_hash = '' WHERE id = $1`, uid)
		login, err := a.CredentialsSet(ctx, uid, "", login, password, "", "cli")
		if err != nil {
			return err
		}
		fmt.Printf("Логин: %s\nПароль: %s\n", login, password)
		return nil
	case "owner":
		if len(args) < 2 {
			fmt.Println(strings.Join(posta.KV(a, ctx, "admin:owners", []string{}), "\n"))
			return nil
		}
		uid, remove := args[1], flag(args, "--remove")
		if !a.UserExists(ctx, uid) {
			return errors.New("нет такого пользователя")
		}
		_, err := posta.KVUpdate(a, ctx, "admin:owners", 0, func(l []string, _ bool) *[]string {
			var out []string
			for _, x := range l {
				if x != uid {
					out = append(out, x)
				}
			}
			if !remove {
				out = append(out, uid)
			}
			return &out
		})
		return err
	case "login-bot":
		if err := need(args, 3, "login-bot <токен> <имя бота>"); err != nil {
			return err
		}
		a.TelegramLoginBotSave(ctx, args[1], strings.TrimPrefix(args[2], "@"))
		return nil
	case "login-bot-from-channel":
		if err := need(args, 2, "login-bot-from-channel <id>"); err != nil {
			return err
		}
		c := a.User(ctx, args[1], nil).TelegramConnection("")
		if c == nil {
			return errors.New("у профиля нет канала с ботом")
		}
		a.TelegramLoginBotSave(ctx, c.Token, c.BotUsername)
		fmt.Println("Бот входа: @" + c.BotUsername)
		return nil
	case "adult-allow":
		if err := need(args, 3, "adult-allow <id> on|off"); err != nil {
			return err
		}
		return a.User(ctx, args[1], nil).SetAdultAllowed(args[2] == "on")
	case "balance-add":
		if err := need(args, 3, "balance-add <id> <₽> [заметка]"); err != nil {
			return err
		}
		rub, err := strconv.ParseFloat(args[2], 64)
		if err != nil {
			return errors.New("сумма — число")
		}
		bal, err := a.BillingTopup(ctx, args[1], rub, strings.Join(args[3:], " "), "")
		fmt.Printf("Баланс: %.2f ₽\n", posta.KopToRub(bal))
		return err
	case "ua-filter":
		if err := need(args, 2, "ua-filter on|off"); err != nil {
			return err
		}
		a.UAFilterSet(ctx, args[1] == "on")
		return nil
	case "discovery-run":
		if err := need(args, 2, "discovery-run <id>"); err != nil {
			return err
		}
		u := a.User(ctx, args[1], print)
		topic := u.CurrentTopic()
		if len(args) > 2 {
			topic = strings.Join(args[2:], " ")
		}
		a.RequestSourceDiscovery()
		u.RunSourceDiscovery(topic, a.TInt("discovery_minutes")*60)
		return nil
	case "search-test":
		if err := need(args, 2, "search-test <запрос> [тема]"); err != nil {
			return err
		}
		uid := a.RadarAccountUID(ctx)
		if uid == "" {
			return errors.New("нет аккаунта для проверки поиска")
		}
		u := a.User(ctx, uid, print)
		topic := ""
		if len(args) > 2 {
			topic = strings.Join(args[2:], " ")
		}
		fmt.Print(u.SearchTest(args[1], topic))
		return nil
	case "discovery-origins":
		topic := ""
		if len(args) > 1 {
			topic = strings.Join(args[1:], " ")
		}
		for kind, s := range a.DiscoveryOriginReport(a.Catalog(), topic) {
			fmt.Println(kind, s)
		}
		return nil
	case "leads-scan":
		uid := a.RadarAccountUID(ctx)
		if len(args) > 1 {
			uid = args[1]
		}
		if uid == "" {
			return errors.New("нет аккаунта радара")
		}
		fmt.Println(jx.M(a.RadarRun(ctx, uid, print)))
		return nil
	case "outbox-repair":
		only := ""
		for _, x := range args[1:] {
			if posta.IsValidUserID(x) {
				only = x
			}
		}
		for _, l := range a.OutboxRepair(ctx, !flag(args, "--dry-run"), only) {
			fmt.Println(l)
		}
		return nil
	case "media-repair":
		if err := need(args, 2, "media-repair <id> [--dry-run]"); err != nil {
			return err
		}
		fmt.Println(a.User(ctx, args[1], nil).MediaRepairUser(flag(args, "--dry-run")))
		return nil
	case "users-gc":
		fmt.Println("Удалено профилей:", a.UsersGC(ctx))
		return nil
	case "users-merge":
		if err := need(args, 3, "users-merge <из> <в>"); err != nil {
			return err
		}
		return a.UserMerge(ctx, args[1], args[2])
	case "llm-preset":
		if err := need(args, 2, "llm-preset free|local|hybrid|cloud"); err != nil {
			return err
		}
		return a.LLMSettingsSave(ctx, llm.Preset(args[1]))
	}
	return fmt.Errorf("неизвестная команда %q (posta help)", args[0])
}
