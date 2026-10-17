// Package review — AI-ревью pull request'а корня курса (день 32, И-15):
// unified diff по файлам и hunk'ам, вход ревью из диапазона git, файла
// патча или GitHub, клиент REST GitHub с подменой httptest, подставные PR
// для стенда и ход агента-ревьюера с проверками кодом.
//
// Этот файл — типы контракта, общие для diff.go, input.go, github.go,
// planted.go, agent.go и markdown.go. Имена — по брифу дня 32, менять
// нельзя: на них ссылаются стенд (bench/t15) и окно (web/review).
package review

import (
	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
)

// Line — строка hunk'а. Kind: ' ' — контекст, '+' — добавлена, '-' —
// удалена. Old и New — номера строки в старом и новом файле; 0 — в этой
// версии строки нет (у добавленной Old = 0, у удалённой New = 0).
type Line struct {
	Kind byte   `json:"kind"`
	Text string `json:"text"`
	Old  int    `json:"old"`
	New  int    `json:"new"`
}

// Hunk — участок diff «@@ -OldStart,OldLines +NewStart,NewLines @@ Header».
type Hunk struct {
	OldStart int    `json:"old_start"`
	OldLines int    `json:"old_lines"`
	NewStart int    `json:"new_start"`
	NewLines int    `json:"new_lines"`
	Header   string `json:"header,omitempty"`
	Lines    []Line `json:"lines"`
}

// Статусы файла в diff.
const (
	StatusAdded    = "added"
	StatusModified = "modified"
	StatusDeleted  = "deleted"
	StatusRenamed  = "renamed"
)

// FileDiff — изменения одного файла. Path — путь в новой версии (у
// удалённого — старый путь), OldPath — путь до переименования (пусто,
// если файл не переименован). Binary — текстового diff нет. Added и
// Removed — число добавленных и удалённых строк.
type FileDiff struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path,omitempty"`
	Status  string `json:"status"` // added | modified | deleted | renamed
	Binary  bool   `json:"binary,omitempty"`
	Hunks   []Hunk `json:"hunks"`
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
}

// Input — PR на входе ревью: заголовок и описание, базовая и головная
// ревизии, диапазон git (base...head), номер PR в GitHub (0 — не из
// GitHub), разобранные файлы и исходный текст diff. Truncated — diff
// обрезан по пределу байт (хвост файлов в Files может отсутствовать).
type Input struct {
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Base      string     `json:"base,omitempty"`
	Head      string     `json:"head,omitempty"`
	Range     string     `json:"range,omitempty"`
	Number    int        `json:"number,omitempty"`
	Files     []FileDiff `json:"files"`
	Diff      string     `json:"diff"`
	Truncated bool       `json:"truncated,omitempty"`
}

// PRInfo — pull request из GitHub: номер, заголовок, описание, базовая
// и головная ветки, sha головы и ссылка.
type PRInfo struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Base    string `json:"base"`
	Head    string `json:"head"`
	HeadSHA string `json:"head_sha"`
	HTMLURL string `json:"html_url"`
}

// Comment — inline-комментарий ревью: файл, строка новой нумерации
// (side RIGHT) и текст.
type Comment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Body string `json:"body"`
}

// PostedReview — уже опубликованное ревью PR (для дедупа по маркеру).
type PostedReview struct {
	ID   int64  `json:"id"`
	Body string `json:"body"`
}

// Evidence — на что опирается находка: для rule и docs — фрагмент
// docs_search этого хода (ChunkID) и дословная цитата из него (Quote);
// для bug — файл и строка, прочитанные read_file в этом ходе или взятые
// из diff (Path, Line).
type Evidence struct {
	ChunkID string `json:"chunk_id,omitempty"`
	Quote   string `json:"quote,omitempty"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
}

// Виды находок.
const (
	KindBug            = "bug"            // ошибка в коде
	KindRule           = "rule"           // нарушение правила проекта (с цитатой документации)
	KindDocs           = "docs"           // расхождение кода и документации (с цитатой)
	KindRecommendation = "recommendation" // рекомендация
)

// Серьёзность находки.
const (
	SeverityMajor = "major"
	SeverityMinor = "minor"
)

// Finding — находка ревью: файл и строка diff (новая нумерация,
// Commentable), вид, серьёзность, текст и доказательство.
type Finding struct {
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Kind     string   `json:"kind"`     // bug | rule | docs | recommendation
	Severity string   `json:"severity"` // major | minor
	Text     string   `json:"text"`
	Evidence Evidence `json:"evidence"`
}

// Вердикты ревью.
const (
	VerdictOK      = "ok"
	VerdictChanges = "changes"
)

// Review — результат хода: находки, сводка и вердикт (ok — находок нет и
// сводка содержит «замечаний нет»; changes — есть что исправить).
type Review struct {
	Findings []Finding `json:"findings"`
	Summary  string    `json:"summary"`
	Verdict  string    `json:"verdict"` // ok | changes
}

// FinishName — имя завершающего инструмента ревью.
const FinishName = "review_done"

// Options — настройки хода ревью: механизмы (review.rag и др.), модель и
// предел байт файла, который агент читает целиком через read_file.
type Options struct {
	Features     features.Set
	Model        string
	MaxFileBytes int
}

// Turn — ход ревью целиком: вход, принятое ревью, проверка цитат,
// сколько результатов код отверг, принято ли «не проверено» после
// отказов, счётчики агента, журнал вызовов, время и выдача docs_search.
type Turn struct {
	Input      Input          `json:"input"`
	Review     Review         `json:"review"`
	Check      cite.CiteCheck `json:"check"`
	Rejected   int            `json:"rejected"`
	Unverified bool           `json:"unverified"`
	Stats      agent.Stats    `json:"stats"`
	Calls      []help.Call    `json:"calls"`
	Seconds    float64        `json:"seconds"`
	Hits       []kb.Hit       `json:"hits,omitempty"`
}

// Planted — подставной PR стенда: патч (имя файла в каталоге planted),
// посаженная проблема — файл, строка новой нумерации и ожидаемый вид
// находки; Clean — проблем нет (ожидается «замечаний нет»). Note — что
// именно посажено и какое правило нарушено (для отчёта стенда).
type Planted struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Body  string `json:"body,omitempty"`
	Patch string `json:"patch"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Clean bool   `json:"clean,omitempty"`
	Note  string `json:"note,omitempty"`
}
