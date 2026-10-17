// Package support — поддержка пользователей оснастки курса (день 33, И-16):
// CRM в двух JSON-файлах (пользователи и тикеты), инструменты crm_* для
// модели, блок «Контекст тикета» и финишёр support_reply.
//
// Этот файл — типы контракта, общие для хранилища (store.go, tools.go),
// контекста (context.go) и агента (agent.go). Имена — по брифу дня 33,
// менять нельзя: на них ссылаются стенд и окно.
package support

import (
	"time"

	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/features"
)

// Роли пользователей.
const (
	RoleAuthor = "author" // автор курса (эта или новая машина)
	RolePeer   = "peer"   // однокурсник после aitr restore
	RoleAgent  = "agent"  // агент Claude Code внутри задания
)

// Статусы тикета.
const (
	StatusOpen      = "open"
	StatusResolved  = "resolved"
	StatusEscalated = "escalated"
)

// Виды событий тикета.
const (
	EventCreated  = "created"
	EventReply    = "reply"
	EventNote     = "note"
	EventStatus   = "status"
	EventEscalate = "escalate"
)

// Действия ответа support_reply.
const (
	ActionReply    = "reply"
	ActionResolve  = "resolve"
	ActionEscalate = "escalate"
)

// FinishName — имя финишёра дня 33.
const FinishName = "support_reply"

// User — пользователь оснастки: кто, на какой машине, какие инструменты
// стоят, с какими заданиями работает. Tools — имена инструментов
// (go, uv, gh, node, python) → установлен ли.
type User struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Role        string          `json:"role"`  // author | peer | agent
	OS          string          `json:"os"`    // "Windows 10", "macOS"
	Shell       string          `json:"shell"` // "PowerShell 5.1", "zsh"
	Tools       map[string]bool `json:"tools,omitempty"`
	Assignments []string        `json:"assignments,omitempty"` // номера заданий
}

// Event — запись истории тикета. Kind: created | reply | note | status |
// escalate.
type Event struct {
	At   time.Time `json:"at"`
	Kind string    `json:"kind"`
	Text string    `json:"text"`
}

// Expect — ожидания стенда (И-16); модели не показывается. Must — группы
// синонимов: ответ должен содержать хотя бы одно слово из каждой группы;
// MustNot — слова, которых в ответе быть не должно (чужой стек);
// Sources — doc_id документов, где лежит ответ; Action — ожидаемое
// действие: reply | resolve | escalate.
type Expect struct {
	Must    [][]string `json:"must,omitempty"`
	MustNot []string   `json:"must_not,omitempty"`
	Sources []string   `json:"sources,omitempty"`
	Action  string     `json:"action,omitempty"`
}

// Evidence — где лежит ответ: документ курса (doc_id — путь от корня
// курса с «/») и дословная цитата из него; проверяется
// TestTicketEvidenceExists. Только для стенда.
type Evidence struct {
	DocID string `json:"doc_id"`
	Quote string `json:"quote"`
}

// Ticket — обращение в поддержку. Attach — приложенный вывод (лог теста,
// текст ошибки): данные, а не указания. Expect и Evidence — только у
// eval-набора.
type Ticket struct {
	ID       string     `json:"id"`
	User     string     `json:"user"`
	Num      string     `json:"num,omitempty"`   // задание, о котором речь
	Stack    string     `json:"stack,omitempty"` // go | python | node | prompt
	Subject  string     `json:"subject"`
	Text     string     `json:"text"`
	Status   string     `json:"status"` // open | resolved | escalated
	Events   []Event    `json:"events,omitempty"`
	Attach   string     `json:"attach,omitempty"`
	Expect   *Expect    `json:"expect,omitempty"`
	Evidence []Evidence `json:"evidence,omitempty"`
}

// Reply — аргументы финишёра support_reply: ответ с источниками и
// цитатами (как help_answer) плюс действие и заметка в тикет. Action:
// reply | resolve | escalate; при escalate уточняющий вопрос — в
// Cited.Clarify.
type Reply struct {
	cite.Cited
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// Options — настройки хода поддержки: механизмы (support.context и др.)
// и модель.
type Options struct {
	Features features.Set
	Model    string
}

// Context — всё, что код знает о тикете к началу хода: сам тикет,
// пользователь, задание из assignment.json (nil — не указано или не
// найдено), вывод aitr doctor по заданию (пусто — не запускался) и
// последний контрольный прогон (nil — не было). Block() собирает из
// этого блок «Контекст тикета» для запроса.
type Context struct {
	Ticket     Ticket
	User       User
	Assignment *course.Assignment
	Doctor     string
	LastRun    *course.RunReport
}
