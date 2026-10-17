package web

// Вкладка «Тикеты» (день 33): список тикетов CRM, карточка тикета с
// блоком контекста, ответ ассистента по SSE (журнал + ответ, как /api/ask)
// и две кнопки, которыми человек подтверждает действие — «Закрыть» и
// «Передать». Статус тикета меняет только код по этим кнопкам (ИП-7):
// инструмента crm_status у модели нет.
//
// Окно не знает устройства агента поддержки: ход и сбор контекста приходят
// замыканиями (serve.go подставляет support.Run и support.Gather, тесты —
// подставки), CRM — интерфейсом, которому удовлетворяет *support.Store.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/support"
	"aitr/34-files-agent/internal/tools"
)

// SupportStore — что окну нужно от CRM; *support.Store подходит.
type SupportStore interface {
	Tickets() []support.Ticket
	Ticket(id string) (support.Ticket, bool)
	User(id string) (support.User, bool)
	AddNote(id, text string) error
	SetStatus(id, status, text string) error
}

// SupportTurn — итог хода поддержки, как его видит окно: ответ финишёра
// support_reply, проверка цитат кодом, счётчики, блок контекста, журнал
// вызовов и выдача docs_search хода (для заголовков источников; пусто —
// заголовки берутся из базы по chunk_id).
type SupportTurn struct {
	Reply    support.Reply
	Check    cite.CiteCheck
	Stats    agent.Stats
	Context  string
	Calls    []help.Call
	Hits     []kb.Hit
	Features features.Set
	// Applied — что код сделал с тикетом после принятого ответа (resolved,
	// escalated, reply); пусто — ничего.
	Applied string
}

// Support — поддержка тикетов в окне. Store nil — вкладка отвечает 503.
type Support struct {
	Store SupportStore
	// Run — ход поддержки по тикету; события хода — в em. nil — модель не
	// настроена (503).
	Run func(ctx context.Context, id string, em agent.Emitter) (*SupportTurn, error)
	// Context — блок «Контекст тикета» для карточки; nil — без блока.
	Context func(ctx context.Context, id string) (string, error)
}

// TicketRow — строка списка тикетов.
type TicketRow struct {
	ID       string `json:"id"`
	Subject  string `json:"subject"`
	User     string `json:"user"`
	UserName string `json:"user_name,omitempty"`
	Role     string `json:"role,omitempty"`
	Num      string `json:"num,omitempty"`
	Stack    string `json:"stack,omitempty"`
	Status   string `json:"status"`
}

// TicketView — карточка тикета: тикет без ожиданий стенда, пользователь и
// блок контекста (Note — почему блока нет).
type TicketView struct {
	Ticket  support.Ticket `json:"ticket"`
	User    *support.User  `json:"user,omitempty"`
	Context string         `json:"context,omitempty"`
	Note    string         `json:"note,omitempty"`
}

// SupportView — ответ окну на /ask: как AnswerView, плюс действие и
// заметка финишёра, блок контекста и тикет после хода.
type SupportView struct {
	Ticket     support.Ticket `json:"ticket"`
	Text       string         `json:"text"`
	Reply      support.Reply  `json:"reply"`
	Action     string         `json:"action"`
	Applied    string         `json:"applied,omitempty"`
	Check      cite.CiteCheck `json:"check"`
	Stats      agent.Stats    `json:"stats"`
	Sources    []SourceView   `json:"sources"`
	Unverified bool           `json:"unverified"`
	Context    string         `json:"context,omitempty"`
	Features   features.Set   `json:"features"`
	Calls      []help.Call    `json:"calls,omitempty"`
	Error      string         `json:"error,omitempty"`
	Seconds    float64        `json:"seconds"`
}

// supportRoutes — маршруты вкладки «Тикеты».
func (s *Server) supportRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/support/tickets", s.supportList)
	mux.HandleFunc("GET /api/support/tickets/{id}", s.supportTicket)
	mux.HandleFunc("POST /api/support/tickets/{id}/ask", s.supportAsk)
	mux.HandleFunc("POST /api/support/tickets/{id}/resolve", s.supportSet(support.StatusResolved))
	mux.HandleFunc("POST /api/support/tickets/{id}/escalate", s.supportSet(support.StatusEscalated))
}

// store — CRM или причина, почему её нет.
func (s *Server) store(w http.ResponseWriter) (SupportStore, bool) {
	if s.Support == nil || s.Support.Store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "CRM не открыта: curator serve -crm <каталог>"})
		return nil, false
	}
	return s.Support.Store, true
}

func (s *Server) supportList(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store(w)
	if !ok {
		return
	}
	rows := []TicketRow{}
	for _, t := range st.Tickets() {
		row := TicketRow{ID: t.ID, Subject: t.Subject, User: t.User, Num: t.Num, Stack: t.Stack, Status: t.Status}
		if u, ok := st.User(t.User); ok {
			row.UserName, row.Role = u.Name, u.Role
		}
		rows = append(rows, row)
	}
	writeJSON(w, http.StatusOK, rows)
}

func (s *Server) supportTicket(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	t, ok := st.Ticket(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "тикета нет: " + id})
		return
	}
	v := ticketView(st, t)
	if s.Support.Context != nil {
		block, err := s.Support.Context(r.Context(), id)
		if err != nil {
			v.Note = "контекст тикета не собран: " + err.Error()
		}
		v.Context = block
	}
	writeJSON(w, http.StatusOK, v)
}

// ticketView — тикет без ожиданий стенда (Expect, Evidence — не для окна и
// не для модели) и его пользователь.
func ticketView(st SupportStore, t support.Ticket) TicketView {
	t.Expect, t.Evidence = nil, nil
	v := TicketView{Ticket: t}
	if u, ok := st.User(t.User); ok {
		v.User = &u
	}
	return v
}

// supportSet — кнопка «Закрыть»/«Передать»: человек подтвердил действие,
// код меняет статус (event status с текстом ответа или вопроса) и, если
// есть, добавляет заметку. Тело {text, note} необязательно.
func (s *Server) supportSet(status string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		st, ok := s.store(w)
		if !ok {
			return
		}
		id := r.PathValue("id")
		if _, ok := st.Ticket(id); !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "тикета нет: " + id})
			return
		}
		var in struct {
			Text string `json:"text"`
			Note string `json:"note"`
		}
		if r.Body != nil {
			if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело запроса: " + err.Error()})
				return
			}
		}
		text := strings.TrimSpace(in.Text)
		if text == "" {
			text = map[string]string{
				support.StatusResolved:  "закрыт в окне куратора",
				support.StatusEscalated: "передан человеку из окна куратора",
			}[status]
		}
		if err := st.SetStatus(id, status, text); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if note := strings.TrimSpace(in.Note); note != "" {
			if err := st.AddNote(id, note); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
				return
			}
		}
		t, _ := st.Ticket(id)
		writeJSON(w, http.StatusOK, ticketView(st, t))
	}
}

// supportAsk — «Ответить ассистентом»: SSE как /api/ask — event: journal
// (agent.Event), event: answer (SupportView), event: error.
func (s *Server) supportAsk(w http.ResponseWriter, r *http.Request) {
	st, ok := s.store(w)
	if !ok {
		return
	}
	id := r.PathValue("id")
	if _, ok := st.Ticket(id); !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "тикета нет: " + id})
		return
	}
	if s.Support.Run == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "модель не настроена: нужен DEEPSEEK_API_KEY или -llm local"})
		return
	}
	em, ok := openSSE(w)
	if !ok {
		return
	}
	started := time.Now()
	turn, err := s.Support.Run(r.Context(), id, em)
	if turn == nil {
		if err == nil {
			err = errors.New("ход не дал ответа")
		}
		em.send("error", map[string]string{"error": err.Error()})
		return
	}
	view := s.supportView(r.Context(), st, id, turn)
	view.Seconds = time.Since(started).Seconds()
	if err != nil {
		view.Error = err.Error()
	}
	em.send("answer", view)
}

// supportView — ответ окну: источники по выдаче хода, а чего в выдаче
// нет — по базе (chunk_id → заголовок и раздел); тикет — уже после хода
// (Run мог добавить заметку).
func (s *Server) supportView(ctx context.Context, st SupportStore, id string, t *SupportTurn) SupportView {
	v := SupportView{Text: t.Reply.Cited.Text(), Reply: t.Reply, Action: t.Reply.Action, Applied: t.Applied, Check: t.Check, Stats: t.Stats,
		Unverified: t.Check.Unverified, Context: t.Context, Features: t.Features}
	if v.Action == "" {
		v.Action = support.ActionReply
	}
	for _, c := range t.Calls {
		c.Result = tools.Truncate(c.Result, 2000)
		v.Calls = append(v.Calls, c)
	}
	v.Sources = sourcesOf(t.Reply.Cited, t.Hits)
	if s.Deps.Store != nil {
		for i := range v.Sources {
			sv := &v.Sources[i]
			if sv.DocID != "" {
				continue
			}
			c, err := s.Deps.Store.Chunk(ctx, sv.ChunkID)
			if err != nil {
				continue
			}
			sv.DocID, sv.Title, sv.Section, sv.Text, sv.Path = c.DocID, c.Title, c.Section, c.Text, pathOf(c)
		}
	}
	if tk, ok := st.Ticket(id); ok {
		tk.Expect, tk.Evidence = nil, nil
		v.Ticket = tk
	}
	return v
}

// sourcesOf — источники ответа по порядку sources, затем фрагменты цитат,
// которых в sources нет; заголовки — из выдачи хода.
func sourcesOf(c cite.Cited, hits []kb.Hit) []SourceView {
	byID := map[string]kb.Hit{}
	for _, h := range hits {
		byID[h.ID] = h
	}
	order := []string{}
	seen := map[string]bool{}
	for _, src := range c.Sources {
		if id := strings.TrimSpace(src.ChunkID); id != "" && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	for _, q := range c.Quotes {
		if id := strings.TrimSpace(q.ChunkID); id != "" && !seen[id] {
			seen[id] = true
			order = append(order, id)
		}
	}
	out := []SourceView{}
	for i, id := range order {
		sv := SourceView{N: i + 1, ChunkID: id, Title: id}
		if h, ok := byID[id]; ok {
			sv.DocID, sv.Title, sv.Section, sv.Text, sv.Path = h.DocID, h.Title, h.Section, h.Text, pathOf(h.Chunk)
		}
		for _, q := range c.Quotes {
			if strings.TrimSpace(q.ChunkID) == id && strings.TrimSpace(q.Text) != "" {
				sv.Quotes = append(sv.Quotes, q.Text)
			}
		}
		out = append(out, sv)
	}
	return out
}

// pathOf — «Раздел › Подраздел» чанка; без пути — его раздел.
func pathOf(c kb.Chunk) string {
	if len(c.Path) > 0 {
		return strings.Join(c.Path, " › ")
	}
	return c.Section
}

// openSSE — заголовки потока событий; false — ответ уже отдан (поток не
// поддерживается).
func openSSE(w http.ResponseWriter) (*sse, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "поток SSE не поддерживается"})
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	return &sse{w: w, fl: fl}, true
}
