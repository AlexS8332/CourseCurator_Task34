// Package web — окно куратора: страница, вопрос по SSE с журналом хода и
// состояние индекса. Сервер тонкий: агент — help.Run, журнал — события
// agent.Event как есть (окно само решает, что показать), ответ — Turn с
// заголовками источников из выдачи хода.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
	page "aitr/34-files-agent/web"
)

// maxBody — предел тела запроса /api/ask.
const maxBody = 64 << 10

// Server — окно куратора.
type Server struct {
	Deps project.Deps
	LLM  llm.Chatter
	// Page — страница; пусто — встроенная web/index.html.
	Page []byte
	// Model — модель для агента; пусто — llm.DefaultModel.
	Model string
	// Features — набор механизмов; пустой — умолчания реестра.
	Features features.Set
	// Tools — инструменты состояния вместо project.Tools (тесты).
	Tools []tools.Tool
	// State — состояние для /api/state; nil — по Deps.Store (без ветки и
	// проверки свежести: их знает тот, кто поднял сервер).
	State func(ctx context.Context) (State, error)
	// Support — вкладка «Тикеты» (support.go); nil — её маршруты отвечают 503.
	Support *Support
	// Review — вкладка «Ревью» (review.go); nil — её маршруты отвечают 503.
	Review *Review
	// Files — вкладка «Файлы» (files.go); nil — её маршруты отвечают 503.
	Files *Files
}

// State — ответ /api/state.
type State struct {
	Index  IndexState `json:"index"`
	Stale  bool       `json:"stale"`
	Branch string     `json:"branch,omitempty"`
	Model  string     `json:"model,omitempty"`
	// Note — что сказать человеку словами (индекса нет, базы нет).
	Note string `json:"note,omitempty"`
}

// IndexState — индекс документации.
type IndexState struct {
	Docs   int    `json:"docs"`
	Chunks int    `json:"chunks"`
	Commit string `json:"commit,omitempty"`
}

// AnswerView — ответ окну: Turn плюс источники с заголовками.
type AnswerView struct {
	Text          string       `json:"text"`
	Answer        help.Answer  `json:"answer"`
	Check         any          `json:"check"`
	Stats         agent.Stats  `json:"stats"`
	Sources       []SourceView `json:"sources"`
	StateProblems []string     `json:"state_problems,omitempty"`
	Unverified    bool         `json:"unverified"`
	Features      features.Set `json:"features"`
	Calls         []help.Call  `json:"calls,omitempty"`
	Error         string       `json:"error,omitempty"`
	Seconds       float64      `json:"seconds"`
}

// SourceView — источник с заголовком «Документ › Раздел» и цитатами.
type SourceView struct {
	N       int      `json:"n"`
	ChunkID string   `json:"chunk_id"`
	DocID   string   `json:"doc_id,omitempty"`
	Title   string   `json:"title"`
	Section string   `json:"section,omitempty"`
	Path    string   `json:"path,omitempty"`
	Quotes  []string `json:"quotes,omitempty"`
	Text    string   `json:"text,omitempty"`
}

// Handler — маршруты окна.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.page)
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("POST /api/ask", s.ask)
	s.supportRoutes(mux)
	s.reviewRoutes(mux)
	s.filesRoutes(mux)
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	body := s.Page
	if len(body) == 0 {
		body = page.Index
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; img-src 'self' data:")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(body)
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	fn := s.State
	if fn == nil {
		fn = s.defaultState
	}
	st, err := fn(r.Context())
	if err != nil {
		st.Note = joinNote(st.Note, err.Error())
	}
	if st.Model == "" {
		st.Model = s.model()
	}
	writeJSON(w, http.StatusOK, st)
}

// defaultState — число документов и чанков по базе; без ветки и свежести.
func (s *Server) defaultState(ctx context.Context) (State, error) {
	st := State{}
	if s.Deps.Store == nil {
		st.Note = "база документации не открыта"
		return st, nil
	}
	docs, err := s.Deps.Store.Docs(ctx)
	if err != nil {
		return st, err
	}
	st.Index.Docs = len(docs)
	idx, err := s.Deps.Store.Index(ctx, orIndex(s.Deps.Index))
	if err != nil {
		if errors.Is(err, kb.ErrNoIndex) {
			st.Note = "индекс не собран: curator index"
			return st, nil
		}
		return st, err
	}
	st.Index.Chunks = idx.Chunks
	return st, nil
}

func orIndex(s string) string {
	if strings.TrimSpace(s) == "" {
		return string(kb.Structure)
	}
	return s
}

func (s *Server) model() string {
	if s.Model != "" {
		return s.Model
	}
	return llm.DefaultModel
}

// ask — вопрос → SSE: event: journal (каждое agent.Event), event: answer
// (AnswerView), event: error (ход не удался и ответа нет).
func (s *Server) ask(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело запроса: " + err.Error()})
		return
	}
	q := help.Clean(in.Text)
	if q == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустой вопрос"})
		return
	}
	if s.LLM == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "модель не настроена: нужен DEEPSEEK_API_KEY или -llm local"})
		return
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "поток SSE не поддерживается"})
		return
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()

	em := &sse{w: w, fl: fl}
	started := time.Now()
	turn, err := help.Run(r.Context(), s.LLM, s.Deps, q, em, help.Options{Model: s.model(), Features: s.Features, Tools: s.Tools})
	if turn == nil {
		em.send("error", map[string]string{"error": err.Error()})
		return
	}
	view := ViewOf(turn)
	view.Seconds = time.Since(started).Seconds()
	if err != nil {
		view.Error = err.Error()
	}
	em.send("answer", view)
}

// ViewOf — ответ окну из итога хода: источники — sourcesOf (по порядку
// sources, затем фрагменты цитат, которых в sources нет; заголовки — из
// выдачи хода).
func ViewOf(t *help.Turn) AnswerView {
	v := AnswerView{Text: t.Text, Answer: t.Answer, Check: t.Check, Stats: t.Stats, Sources: []SourceView{},
		StateProblems: t.StateProblems, Unverified: t.Unverified(), Features: t.Features}
	if t.Calls != nil {
		for _, c := range t.Calls.Calls() {
			c.Result = tools.Truncate(c.Result, 2000)
			v.Calls = append(v.Calls, c)
		}
	}
	v.Sources = sourcesOf(t.Answer.Cited, t.Hits)
	return v
}

// sse — эмиттер журнала в поток событий. Безопасен для нескольких горутин.
type sse struct {
	mu sync.Mutex
	w  http.ResponseWriter
	fl http.Flusher
}

func (s *sse) Log(e agent.Event)      { s.send("journal", e) }
func (s *sse) Publish(u agent.Update) { s.send("update", u) }

func (s *sse) send(event string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		data, _ = json.Marshal(map[string]string{"error": err.Error()})
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, data)
	s.fl.Flush()
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func joinNote(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}
