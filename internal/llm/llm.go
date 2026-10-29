// Package llm — модели Posta: локальная Ollama и OpenRouter за общим интерфейсом, выбор по ролям задач.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Role — вид задачи. Для каждой роли в админке выбирается провайдер, модель и резерв.
type Role string

const (
	RolePrescreen Role = "prescreen" // быстрый отсев мусора по картинке
	RoleAnalysis  Role = "analysis"  // полный анализ записи по теме
	RoleJudge     Role = "judge"     // «есть ли шутка», выбор подтемы: короткий ответ по картинке
	RoleSmart     Role = "smart"     // профиль темы, классификация, план подбора, проверка канала
	RoleText      Role = "text"      // пачки текстовых записей, подписи постов, стиль канала
	RoleRadar     Role = "radar"     // классификация сообщений радара лидов
	RoleTranslate Role = "translate" // перевод (если перевод моделью включён)
	RoleEmbed     Role = "embed"     // эмбеддинги: сходство тем
)

var Roles = []Role{RolePrescreen, RoleAnalysis, RoleJudge, RoleSmart, RoleText, RoleRadar, RoleTranslate, RoleEmbed}

var RoleTitles = map[Role]string{
	RolePrescreen: "Быстрая оценка (мусор ли)",
	RoleAnalysis:  "Полный анализ записи",
	RoleJudge:     "Проверка шутки и подтемы",
	RoleSmart:     "Умные задачи (профиль темы, подбор)",
	RoleText:      "Тексты и подписи",
	RoleRadar:     "Радар лидов",
	RoleTranslate: "Перевод",
	RoleEmbed:     "Сходство тем (эмбеддинги)",
}

// Vision — роли, которым нужны картинки.
func (r Role) Vision() bool { return r == RolePrescreen || r == RoleAnalysis || r == RoleJudge || r == RoleSmart }

type Request struct {
	Role        Role
	Prompt      string
	System      string
	Images      [][]byte       // JPEG/PNG; уменьшать заранее
	Schema      map[string]any // JSON Schema ответа (structured output); nil — свободный текст
	JSON        bool           // ответ JSON-объектом без схемы
	MaxTokens   int
	Temperature float64
	Seed        *int
	Think       bool // разрешить размышления (если модель умеет)
	Logprobs    bool // вероятности первых токенов ответа (для «да/нет» и выбора буквы)
	TopLogprobs int
	Timeout     time.Duration
	UserID      string // для журнала и статистики
	Topic       string
	Quiet       bool                // не писать обмен в журнал (служебные проверки)
	Private     bool                // только локально: 18+ и прочее, что нельзя отправлять в облако
	OnThinking  func(chunk string) // размышления по мере генерации
}

type Alt struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
}

type TokenLogprob struct {
	Token   string  `json:"token"`
	Logprob float64 `json:"logprob"`
	Top     []Alt   `json:"top_logprobs"`
}

type Response struct {
	Text             string
	Thinking         string
	Logprobs         []TokenLogprob
	Provider, Model  string
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	CostUSD          float64
	Duration         time.Duration
	LoadSeconds      float64 // Ollama: загрузка модели
	PromptSeconds    float64
	AnswerSeconds    float64
}

// Provider — источник моделей.
type Provider interface {
	Name() string
	Generate(ctx context.Context, model string, req *Request) (*Response, error)
	Embed(ctx context.Context, model string, texts []string) ([][]float64, error)
}

// ErrUnsupported — у провайдера нет такой возможности (например, logprobs у модели).
var ErrUnsupported = errors.New("не поддерживается")

// ---- Разбор ответов ----

var fenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

// DecodeJSON — объект из ответа модели: терпит ```json, текст вокруг и оборванный хвост (тогда спасает relevance).
func DecodeJSON(reply string) map[string]any {
	var out map[string]any
	if json.Unmarshal([]byte(reply), &out) == nil {
		return out
	}
	if m := fenceRe.FindStringSubmatch(reply); m != nil && json.Unmarshal([]byte(m[1]), &out) == nil {
		return out
	}
	start, end := strings.Index(reply, "{"), strings.LastIndex(reply, "}")
	if start < 0 {
		return nil
	}
	if end > start && json.Unmarshal([]byte(reply[start:end+1]), &out) == nil {
		return out
	}
	return salvageAnalysis(reply)
}

var (
	salvageRelRe = regexp.MustCompile(`"relevance"\s*:\s*(\d{1,3})`)
	salvageStrRe = map[string]*regexp.Regexp{
		"description": regexp.MustCompile(`"description"\s*:\s*"((?:[^"\\]|\\.)*)`),
		"evidence":    regexp.MustCompile(`"evidence"\s*:\s*"((?:[^"\\]|\\.)*)`),
	}
)

// salvageAnalysis — поля из ответа, оборванного лимитом токенов: годится, если relevance уже есть.
func salvageAnalysis(reply string) map[string]any {
	m := salvageRelRe.FindStringSubmatch(reply)
	if m == nil {
		return nil
	}
	rel, _ := strconv.Atoi(m[1])
	out := map[string]any{"relevance": float64(rel), "rating": nil, "description": "", "evidence": ""}
	for f, re := range salvageStrRe {
		if t := re.FindStringSubmatch(reply); t != nil {
			var s string
			if json.Unmarshal([]byte(`"`+t[1]+`"`), &s) == nil {
				out[f] = s
			} else {
				out[f] = t[1]
			}
		}
	}
	return out
}

// YesProbability — P(да) / (P(да) + P(нет)) по вероятностям первого токена, 0–100; nil — данных нет.
func YesProbability(lp []TokenLogprob) *int {
	if len(lp) == 0 {
		return nil
	}
	first := lp[0]
	cands := first.Top
	if len(cands) == 0 {
		cands = []Alt{{Token: first.Token, Logprob: first.Logprob}}
	}
	var yes, no float64
	for _, c := range cands {
		tok := strings.ToLower(strings.Trim(c.Token, " \t\n\"'«»*.,!"))
		p := math.Exp(c.Logprob)
		switch tok {
		case "да", "yes", "д":
			yes += p
		case "нет", "no", "не", "н":
			no += p
		}
	}
	if yes+no <= 0 {
		return nil
	}
	v := ClampScore(100 * yes / (yes + no))
	return &v
}

// LetterProbabilities — вероятности букв-вариантов по первому токену, нормированные; пусто — данных нет.
func LetterProbabilities(lp []TokenLogprob, letters []string) map[string]float64 {
	if len(lp) == 0 {
		return nil
	}
	first := lp[0]
	cands := first.Top
	if len(cands) == 0 {
		cands = []Alt{{Token: first.Token, Logprob: first.Logprob}}
	}
	sum := map[string]float64{}
	total := 0.0
	for _, c := range cands {
		tok := strings.ToUpper(strings.Trim(c.Token, " \t\n\"'«»*.,!()"))
		for _, l := range letters {
			if tok == l {
				p := math.Exp(c.Logprob)
				sum[tok] += p
				total += p
			}
		}
	}
	if total <= 0 {
		return nil
	}
	for k := range sum {
		sum[k] /= total
	}
	return sum
}

func ClampScore(v float64) int {
	n := int(math.Round(v))
	return max(0, min(100, n))
}

var cjkRe = regexp.MustCompile(`[\x{3000}-\x{303f}\x{3040}-\x{30ff}\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{ff00}-\x{ffef}]+`)
var spacesRe = regexp.MustCompile(`[ \t]{2,}`)

// StripCJK — иероглифы и CJK-пунктуация, которые модели иногда подмешивают в русский текст.
func StripCJK(s string) string {
	return strings.TrimSpace(spacesRe.ReplaceAllString(cjkRe.ReplaceAllString(s, ""), " "))
}
