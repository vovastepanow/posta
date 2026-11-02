// Package ch — события и аналитика в ClickHouse. Запись асинхронная, пачками: события копятся в памяти и уходят раз в
// 2 секунды или по 1000 строк. ClickHouse недоступен — события отбрасываются с предупреждением, приложение работает.
package ch

import (
	"context"
	"embed"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/ClickHouse/clickhouse-go/v2/lib/driver"
)

//go:embed migrations/*.sql
var migrations embed.FS

type AICall struct {
	TS               time.Time
	Provider, Model  string
	Role             string
	UserID, Topic    string
	OK               bool
	Error            string
	Images           int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
	LatencyMS        int
}

type Vote struct {
	TS                        time.Time
	UserID, Topic, Vote, Reason string
	SourceKey, Platform       string
	Relevance, CardScore      int
	Format                    string
	Tags                      []string
	ImageKey                  string
}

type Screening struct {
	TS                         time.Time
	UserID, Topic, Stage       string
	SourceKey, Platform        string
	ImageKey                   string
	Score                      int
	Passed                     bool
	Reason, Model              string
}

type LogLine struct {
	TS     time.Time
	UserID string
	Line   string
}

type PublishEvent struct {
	TS                           time.Time
	UserID, ChannelID, Platform  string
	OK                           bool
	Error, PostRef               string
	MediaCount                   int
	Scheduled                    bool
}

// Sink — куда пишутся события. Nil-безопасен: у nil *Sink методы ничего не делают.
type Sink struct {
	conn driver.Conn
	mu   sync.Mutex
	buf  map[string][][]any
	stop chan struct{}
	done chan struct{}
}

var tables = map[string]string{
	"ai_calls":       "ts, provider, model, role, user_id, topic, ok, error, images, prompt_tokens, completion_tokens, cached_tokens, cost_usd, latency_ms",
	"votes":          "ts, user_id, topic, vote, reason, source_key, platform, relevance, card_score, format, tags, image_key",
	"screening":      "ts, user_id, topic, stage, source_key, platform, image_key, score, passed, reason, model",
	"parser_log":     "ts, user_id, line",
	"publish_events": "ts, user_id, channel_id, platform, ok, error, post_ref, media_count, scheduled",
}

// Open подключается к ClickHouse (addr — host:port родного протокола). Пустой addr — nil (без аналитики).
func Open(ctx context.Context, addr, database string) (*Sink, error) {
	if addr == "" {
		return nil, nil
	}
	conn, err := clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Database: "default"},
		DialTimeout: 5 * time.Second, Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}})
	if err != nil {
		return nil, err
	}
	if err := conn.Ping(ctx); err != nil {
		return nil, fmt.Errorf("ClickHouse недоступен: %w", err)
	}
	if err := conn.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+database); err != nil {
		return nil, err
	}
	conn.Close()
	conn, err = clickhouse.Open(&clickhouse.Options{Addr: []string{addr}, Auth: clickhouse.Auth{Database: database},
		DialTimeout: 5 * time.Second, Compression: &clickhouse.Compression{Method: clickhouse.CompressionLZ4}})
	if err != nil {
		return nil, err
	}
	s := &Sink{conn: conn, buf: map[string][][]any{}, stop: make(chan struct{}), done: make(chan struct{})}
	go s.loop()
	return s, nil
}

// Migrate создаёт таблицы (CREATE TABLE IF NOT EXISTS — повторный запуск безопасен).
func (s *Sink) Migrate(ctx context.Context) error {
	if s == nil {
		return nil
	}
	body, _ := migrations.ReadFile("migrations/0001_init.sql")
	for _, stmt := range strings.Split(string(body), ";\n") {
		stmt = strings.TrimSpace(stripComments(stmt))
		if stmt == "" {
			continue
		}
		if err := s.conn.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("ClickHouse: %w", err)
		}
	}
	return nil
}

func stripComments(sql string) string {
	var out []string
	for _, line := range strings.Split(sql, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "--") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}

func (s *Sink) Conn() driver.Conn {
	if s == nil {
		return nil
	}
	return s.conn
}

func (s *Sink) add(table string, row []any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.buf[table] = append(s.buf[table], row)
	n := len(s.buf[table])
	s.mu.Unlock()
	if n >= 1000 {
		go s.flush()
	}
}

func b2u(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func clampU32(n int) uint32 {
	if n < 0 {
		return 0
	}
	return uint32(n)
}

func (s *Sink) AICall(e AICall) {
	s.add("ai_calls", []any{e.TS, e.Provider, e.Model, e.Role, e.UserID, e.Topic, b2u(e.OK), e.Error, uint8(min(e.Images, 255)),
		clampU32(e.PromptTokens), clampU32(e.CompletionTokens), clampU32(e.CachedTokens), e.CostUSD, clampU32(e.LatencyMS)})
}

func (s *Sink) Vote(e Vote) {
	if e.Tags == nil {
		e.Tags = []string{}
	}
	s.add("votes", []any{e.TS, e.UserID, e.Topic, e.Vote, e.Reason, e.SourceKey, e.Platform, int16(e.Relevance), int16(e.CardScore), e.Format, e.Tags, e.ImageKey})
}

func (s *Sink) Screening(e Screening) {
	s.add("screening", []any{e.TS, e.UserID, e.Topic, e.Stage, e.SourceKey, e.Platform, e.ImageKey, int16(e.Score), b2u(e.Passed), e.Reason, e.Model})
}

func (s *Sink) Log(e LogLine) { s.add("parser_log", []any{e.TS, e.UserID, e.Line}) }

func (s *Sink) Publish(e PublishEvent) {
	s.add("publish_events", []any{e.TS, e.UserID, e.ChannelID, e.Platform, b2u(e.OK), e.Error, e.PostRef, uint8(min(e.MediaCount, 255)), b2u(e.Scheduled)})
}

func (s *Sink) loop() {
	defer close(s.done)
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
			s.flush()
		case <-s.stop:
			s.flush()
			return
		}
	}
}

func (s *Sink) flush() {
	s.mu.Lock()
	pending := s.buf
	s.buf = map[string][][]any{}
	s.mu.Unlock()
	for table, rows := range pending {
		if len(rows) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := s.insert(ctx, table, rows)
		cancel()
		if err != nil {
			log.Printf("ClickHouse: %d строк %s не записано: %v", len(rows), table, err)
		}
	}
}

func (s *Sink) insert(ctx context.Context, table string, rows [][]any) error {
	batch, err := s.conn.PrepareBatch(ctx, "INSERT INTO "+table+" ("+tables[table]+")")
	if err != nil {
		return err
	}
	for _, r := range rows {
		if err := batch.Append(r...); err != nil {
			return err
		}
	}
	return batch.Send()
}

// Close дописывает накопленное и закрывает соединение.
func (s *Sink) Close() {
	if s == nil {
		return
	}
	close(s.stop)
	<-s.done
	s.conn.Close()
}
