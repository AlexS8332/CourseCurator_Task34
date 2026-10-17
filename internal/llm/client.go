// Package llm — клиент OpenAI-совместимого API DeepSeek с поддержкой
// вызова инструментов. Пакет ничего не знает об агентах: он умеет отправить
// сообщения с описанием инструментов и вернуть ответ модели как есть —
// текст, список вызовов инструментов и расход токенов.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	DefaultBaseURL = "https://api.deepseek.com"
	DefaultModel   = "deepseek-v4-flash"

	requestTimeout = 3 * time.Minute
	maxBodySize    = 4 << 20
	// thinkMinTokens — наименьший предел ответа у запроса с рассуждением.
	thinkMinTokens = 4096
)

// Роли сообщений в протоколе чата.
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// FinishToolCalls — причина завершения, когда модель хочет вызвать инструменты.
const FinishToolCalls = "tool_calls"

// Message — одно сообщение истории. У ответа модели с вызовами инструментов
// Content бывает пустым, а у ответа инструмента обязателен ToolCallID.
type Message struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
}

// ToolCall — запрос модели на вызов инструмента. Аргументы приходят строкой
// с JSON, разбирает её тот, кто исполняет инструмент.
type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolDef — описание инструмента для модели: имя, назначение и JSON-схема
// аргументов.
type ToolDef struct {
	Type     string      `json:"type"`
	Function FunctionDef `json:"function"`
}

type FunctionDef struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// NewToolDef собирает описание функции в формате API.
func NewToolDef(name, description string, parameters json.RawMessage) ToolDef {
	if len(parameters) == 0 {
		parameters = json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return ToolDef{
		Type: "function",
		Function: FunctionDef{
			Name:        name,
			Description: description,
			Parameters:  parameters,
		},
	}
}

// Usage — расход одного ответа. Completion включает Reasoning: так считает
// провайдер и его прайс.
type Usage struct {
	Prompt     int `json:"prompt"`
	Completion int `json:"completion"`
	Total      int `json:"total"`
	CacheHit   int `json:"cacheHit"`
	CacheMiss  int `json:"cacheMiss"`
	Reasoning  int `json:"reasoning"`
	// Тайминги сервера (v29): загрузка модели, разбор промпта (prefill) и
	// генерация ответа (decode). Заполняет нативный клиент Ollama; облачный
	// API их не отдаёт — нули.
	LoadMS   int64 `json:"loadMs,omitempty"`
	PromptMS int64 `json:"promptMs,omitempty"`
	EvalMS   int64 `json:"evalMs,omitempty"`
}

// Add складывает расход двух ответов.
func (u Usage) Add(o Usage) Usage {
	return Usage{
		Prompt:     u.Prompt + o.Prompt,
		Completion: u.Completion + o.Completion,
		Total:      u.Total + o.Total,
		CacheHit:   u.CacheHit + o.CacheHit,
		CacheMiss:  u.CacheMiss + o.CacheMiss,
		Reasoning:  u.Reasoning + o.Reasoning,
		LoadMS:     u.LoadMS + o.LoadMS,
		PromptMS:   u.PromptMS + o.PromptMS,
		EvalMS:     u.EvalMS + o.EvalMS,
	}
}

// Request — один вызов модели. Tools может быть пустым: тогда это обычный
// запрос-ответ без инструментов.
type Request struct {
	Model       string
	Messages    []Message
	Tools       []ToolDef
	Temperature float64
	MaxTokens   int
	// Think — запросу нужно рассуждение (v27). Клиент с ThinkEffort шлёт
	// его вместо ReasoningEffort; без ThinkEffort признак не действует.
	Think bool
}

// Response — ответ модели. Если модель вызывает инструменты, они лежат
// в Message.ToolCalls, а FinishReason равен FinishToolCalls.
type Response struct {
	Message      Message
	FinishReason string
	Usage        Usage
	Model        string
}

// HasToolCalls — хочет ли модель вызвать инструменты.
func (r Response) HasToolCalls() bool {
	return len(r.Message.ToolCalls) > 0
}

// Chatter — то, что нужно агентам от модели. Интерфейс, а не структура:
// в тестах модель подменяется сценарием без сети.
type Chatter interface {
	Chat(ctx context.Context, req Request) (Response, error)
}

// Client — HTTP-клиент DeepSeek. Ключ в вывод и логи не попадает.
type Client struct {
	APIKey  string
	BaseURL string
	HTTP    *http.Client
	// Thinking включает режим размышлений. Поле отправляется всегда: у
	// моделей V4 умолчание провайдера — «включено», а поведение агента не
	// должно зависеть от умолчаний.
	Thinking bool
	// ReasoningEffort — поле reasoning_effort OpenAI-совместимого API (Ollama,
	// LM Studio, llama.cpp). Пусто — не отправляется. У Qwen3.5 в Ollama
	// рассуждение включено по умолчанию: «none» его выключает, иначе ответ
	// в разы дольше, а при малом max_tokens content остаётся пустым.
	ReasoningEffort string
	// DefaultMaxTokens — предел ответа, если запрос его не задал (v27).
	// Малая локальная модель изредка зацикливается в генерации; без
	// предела она пишет до конца окна — минуты, а запрос падает по таймауту.
	DefaultMaxTokens int
	// ThinkEffort — reasoning_effort для запросов с Think (v27), например
	// «medium». Пусто — Think не действует.
	ThinkEffort string
}

// NewClient собирает клиент с таймаутом на запрос.
func NewClient(apiKey, baseURL string) *Client {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		APIKey:  apiKey,
		BaseURL: baseURL,
		HTTP:    &http.Client{Timeout: requestTimeout},
	}
}

// Формы запроса и ответа API. Отдельные от публичных типов: у провайдера
// свои имена полей и вложенность.

type thinkingOption struct {
	Type string `json:"type"`
}

type apiRequest struct {
	Model           string         `json:"model"`
	Messages        []Message      `json:"messages"`
	Tools           []ToolDef      `json:"tools,omitempty"`
	Temperature     float64        `json:"temperature"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	Stream          bool           `json:"stream"`
	Thinking        thinkingOption `json:"thinking"`
	ReasoningEffort string         `json:"reasoning_effort,omitempty"`
}

type apiUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CacheHitTokens   int `json:"prompt_cache_hit_tokens"`
	CacheMissTokens  int `json:"prompt_cache_miss_tokens"`
	CompletionDetail struct {
		ReasoningTokens int `json:"reasoning_tokens"`
	} `json:"completion_tokens_details"`
}

func (u apiUsage) toUsage() Usage {
	return Usage{
		Prompt:     u.PromptTokens,
		Completion: u.CompletionTokens,
		Total:      u.TotalTokens,
		CacheHit:   u.CacheHitTokens,
		CacheMiss:  u.CacheMissTokens,
		Reasoning:  u.CompletionDetail.ReasoningTokens,
	}
}

type apiResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage apiUsage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat отправляет запрос и возвращает первый вариант ответа.
func (c *Client) Chat(ctx context.Context, req Request) (Response, error) {
	if req.MaxTokens == 0 {
		req.MaxTokens = c.DefaultMaxTokens
	}
	if !req.Think || c.ThinkEffort == "" {
		req.Think = false
		return c.chat(ctx, req, c.ReasoningEffort)
	}
	resp, err := c.chat(ctx, req, c.ThinkEffort)
	if errors.Is(err, ErrEmpty) {
		// Малая модель иногда уходит в рассуждение целиком и не пишет
		// ответа. Один повтор без рассуждения лучше проваленного хода.
		req.Think = false
		return c.chat(ctx, req, c.ReasoningEffort)
	}
	return resp, err
}

// ErrEmpty — модель не вернула ни текста, ни вызовов инструментов.
var ErrEmpty = errors.New("модель вернула пустой ответ")

func (c *Client) chat(ctx context.Context, req Request, effort string) (Response, error) {
	thinking := thinkingOption{Type: "disabled"}
	if c.Thinking {
		thinking.Type = "enabled"
	}
	if req.Think {
		// Рассуждение тратит токены ответа: малый предел оставил бы
		// content пустым.
		if req.MaxTokens > 0 && req.MaxTokens < thinkMinTokens {
			req.MaxTokens = thinkMinTokens
		}
	}

	payload, err := json.Marshal(apiRequest{
		Model:           req.Model,
		Messages:        req.Messages,
		Tools:           req.Tools,
		Temperature:     req.Temperature,
		MaxTokens:       req.MaxTokens,
		Stream:          false,
		Thinking:        thinking,
		ReasoningEffort: effort,
	})
	if err != nil {
		return Response{}, fmt.Errorf("сборка запроса: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.BaseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return Response{}, fmt.Errorf("создание запроса: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)

	client := c.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}

	resp, err := client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("запрос к %s: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return Response{}, fmt.Errorf("чтение ответа: %w", err)
	}

	var parsed apiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil && resp.StatusCode == http.StatusOK {
		return Response{}, fmt.Errorf("разбор ответа: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil && parsed.Error.Message != "" {
			return Response{}, fmt.Errorf("API вернул %s: %s", resp.Status, parsed.Error.Message)
		}
		return Response{}, fmt.Errorf("API вернул %s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	if len(parsed.Choices) == 0 {
		return Response{}, fmt.Errorf("модель не вернула ни одного варианта ответа")
	}

	choice := parsed.Choices[0]
	msg := choice.Message
	msg.Role = RoleAssistant
	msg.Content = strings.TrimSpace(msg.Content)

	// Пустой ответ без вызовов инструментов — ошибка протокола: модели
	// нечего сказать и нечего сделать.
	if msg.Content == "" && len(msg.ToolCalls) == 0 {
		return Response{}, ErrEmpty
	}

	return Response{
		Message:      msg,
		FinishReason: choice.FinishReason,
		Usage:        parsed.Usage.toUsage(),
		Model:        parsed.Model,
	}, nil
}
