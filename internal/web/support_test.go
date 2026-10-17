package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/support"
)

// memStore — CRM в памяти: тикеты и пользователи по образцу eval/support.
type memStore struct {
	mu      sync.Mutex
	users   map[string]support.User
	tickets []support.Ticket
}

func newMemStore() *memStore {
	return &memStore{
		users: map[string]support.User{
			"u-agent": {ID: "u-agent", Name: "агент Claude Code в задании 31", Role: "agent", OS: "Windows 10", Shell: "PowerShell 5.1", Assignments: []string{"31"}},
			"u-peer":  {ID: "u-peer", Name: "однокурсник", Role: "peer", OS: "macOS", Shell: "zsh"},
		},
		tickets: []support.Ticket{
			{ID: "T-08", User: "u-agent", Num: "31", Stack: "go", Subject: "Почему не работает авторизация?", Text: "curator help отвечает 401",
				Status: "open", Attach: "401 Unauthorized", Events: []support.Event{{At: time.Now(), Kind: "created", Text: "создан"}},
				Expect:   &support.Expect{Must: [][]string{{"DEEPSEEK_API_KEY"}}, Action: "resolve"},
				Evidence: []support.Evidence{{DocID: ".claude/skills/aitr/SKILL.md", Quote: "ключи"}}},
			{ID: "T-11", User: "u-peer", Subject: "Как развернуть Kubernetes для задания?", Text: "хочу кластер", Status: "open"},
		},
	}
}

func (m *memStore) Tickets() []support.Ticket {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]support.Ticket(nil), m.tickets...)
}
func (m *memStore) Ticket(id string) (support.Ticket, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, t := range m.tickets {
		if t.ID == id {
			return t, true
		}
	}
	return support.Ticket{}, false
}
func (m *memStore) User(id string) (support.User, bool) {
	u, ok := m.users[id]
	return u, ok
}
func (m *memStore) AddNote(id, text string) error {
	return m.event(id, "", "note", text)
}
func (m *memStore) SetStatus(id, status, text string) error {
	return m.event(id, status, "status", text)
}
func (m *memStore) event(id, status, kind, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.tickets {
		if m.tickets[i].ID == id {
			if status != "" {
				m.tickets[i].Status = status
			}
			m.tickets[i].Events = append(m.tickets[i].Events, support.Event{At: time.Now(), Kind: kind, Text: text})
			return nil
		}
	}
	return errors.New("тикета нет: " + id)
}

// fakeRun — ход поддержки на подставке: в журнал — crm_ticket и aitr_doctor,
// ответ — resolve с источником из выдачи (T-08) или escalate (прочие).
func fakeRun(ctx context.Context, id string, em agent.Emitter) (*SupportTurn, error) {
	em.Log(agent.Event{Kind: "tool.call", Tool: "crm_ticket", CallID: "call_0_crm_ticket", Detail: `{"id":"` + id + `"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "crm_ticket", CallID: "call_0_crm_ticket", Title: "тикет"})
	em.Log(agent.Event{Kind: "tool.call", Tool: "aitr_doctor", CallID: "call_1_aitr_doctor", Detail: `{"num":"31"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "aitr_doctor", CallID: "call_1_aitr_doctor", Title: "DEEPSEEK_API_KEY - не задан"})
	calls := []help.Call{
		{Tool: "crm_ticket", CallID: "call_0_crm_ticket", Args: `{"id":"` + id + `"}`, Result: `{"id":"` + id + `"}`},
		{Tool: "aitr_doctor", CallID: "call_1_aitr_doctor", Args: `{"num":"31"}`, Result: "DEEPSEEK_API_KEY - не задан"},
	}
	if id != "T-08" {
		return &SupportTurn{
			Reply:   support.Reply{Cited: cite.Cited{Status: "unknown", Answer: "", Clarify: "Какой именно кластер и для чего?"}, Action: "escalate"},
			Check:   cite.CiteCheck{OK: true},
			Context: "Контекст тикета: однокурсник, macOS",
			Calls:   calls,
		}, nil
	}
	chunk := kb.Chunk{ID: ".claude/skills/aitr/SKILL.md/structure/007", DocID: ".claude/skills/aitr/SKILL.md", Title: "aitr", Section: "Секреты", Path: []string{"Секреты"},
		Text: "Ключи лежат в .env в корне курса; aitr doctor показывает, какие заданы."}
	return &SupportTurn{
		Reply: support.Reply{Cited: cite.Cited{Status: "answered", Answer: "Ключ DEEPSEEK_API_KEY не задан: aitr doctor 31 это показал. Добавь его в .env в корне курса.",
			Sources: []cite.CitedSource{{ChunkID: chunk.ID}},
			Quotes:  []cite.CitedQuote{{ChunkID: chunk.ID, Text: "aitr doctor показывает, какие заданы"}}},
			Action: "resolve", Note: "ключ не задан — см. aitr doctor"},
		Check:   cite.CiteCheck{OK: true, HasSources: true, HasQuotes: true},
		Stats:   agent.Stats{Steps: 2, ToolCalls: 2},
		Context: "Контекст тикета: агент Claude Code, задание 31, DEEPSEEK_API_KEY — не задан",
		Calls:   calls,
		Hits:    []kb.Hit{{Chunk: chunk, Score: 0.8, Rank: 1}},
	}, nil
}

func supportServer(t *testing.T, st SupportStore, run func(context.Context, string, agent.Emitter) (*SupportTurn, error)) (*httptest.Server, *Server) {
	t.Helper()
	s := &Server{LLM: &llmtest.Fake{}, Model: "fake"}
	if st != nil {
		s.Support = &Support{Store: st, Run: run, Context: func(ctx context.Context, id string) (string, error) {
			if id == "T-11" {
				return "", errors.New("aitr doctor: задание не указано")
			}
			return "Контекст тикета: агент Claude Code, задание 31, DEEPSEEK_API_KEY — не задан", nil
		}}
	}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s
}

func getJSON(t *testing.T, url string, v any) int {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func postJSON(t *testing.T, url, body string, v any) int {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if v != nil && resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(v); err != nil {
			t.Fatalf("%s: %v", url, err)
		}
	}
	return resp.StatusCode
}

func TestSupportListAndTicket(t *testing.T) {
	srv, _ := supportServer(t, newMemStore(), fakeRun)

	var rows []TicketRow
	if code := getJSON(t, srv.URL+"/api/support/tickets", &rows); code != 200 {
		t.Fatalf("список: код %d", code)
	}
	if len(rows) != 2 || rows[0].ID != "T-08" || rows[0].Status != "open" || rows[0].UserName == "" || rows[0].Role != "agent" {
		t.Errorf("список: %+v", rows)
	}

	var v TicketView
	if code := getJSON(t, srv.URL+"/api/support/tickets/T-08", &v); code != 200 {
		t.Fatalf("тикет: код %d", code)
	}
	if v.Ticket.Subject != "Почему не работает авторизация?" || v.User == nil || v.User.Role != "agent" {
		t.Errorf("тикет: %+v", v)
	}
	if !strings.Contains(v.Context, "DEEPSEEK_API_KEY") || v.Note != "" {
		t.Errorf("контекст: %q, note %q", v.Context, v.Note)
	}
	if v.Ticket.Expect != nil || v.Ticket.Evidence != nil {
		t.Error("ожидания стенда ушли в окно")
	}
	// Контекст не собрался — тикет всё равно отдаётся, с заметкой почему.
	v = TicketView{}
	if code := getJSON(t, srv.URL+"/api/support/tickets/T-11", &v); code != 200 {
		t.Fatalf("тикет: код %d", code)
	}
	if v.Context != "" || !strings.Contains(v.Note, "doctor") {
		t.Errorf("T-11: %+v", v)
	}
	if code := getJSON(t, srv.URL+"/api/support/tickets/T-99", nil); code != http.StatusNotFound {
		t.Errorf("неизвестный тикет: код %d", code)
	}
}

func TestSupportAskStreamsJournalAndAnswer(t *testing.T) {
	st := newMemStore()
	srv, _ := supportServer(t, st, fakeRun)

	resp, err := http.Post(srv.URL+"/api/support/tickets/T-08/ask", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q, код %d", ct, resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	evs := parseSSE(string(body))
	tools := map[string]bool{}
	var view SupportView
	answers := 0
	for _, e := range evs {
		switch e.name {
		case "journal":
			var ev struct {
				Kind string `json:"kind"`
				Tool string `json:"tool"`
			}
			if err := json.Unmarshal([]byte(e.data), &ev); err != nil {
				t.Errorf("событие не JSON: %s", e.data)
			}
			if ev.Kind == "tool.call" {
				tools[ev.Tool] = true
			}
		case "answer":
			answers++
			if err := json.Unmarshal([]byte(e.data), &view); err != nil {
				t.Fatalf("ответ не разобран: %v: %s", err, e.data)
			}
		case "error":
			t.Fatalf("ошибка хода: %s", e.data)
		}
	}
	if answers != 1 || !tools["crm_ticket"] || !tools["aitr_doctor"] {
		t.Fatalf("ответов %d, вызовы %v", answers, tools)
	}
	if view.Action != "resolve" || view.Reply.Status != "answered" || view.Unverified || !strings.Contains(view.Text, "DEEPSEEK_API_KEY") {
		t.Errorf("ответ: %+v", view)
	}
	if len(view.Sources) != 1 || view.Sources[0].Path != "Секреты" || view.Sources[0].Title != "aitr" || len(view.Sources[0].Quotes) != 1 {
		t.Errorf("источники: %+v", view.Sources)
	}
	if len(view.Calls) != 2 || view.Calls[1].Tool != "aitr_doctor" || !strings.Contains(view.Context, "не задан") {
		t.Errorf("вызовы %+v, контекст %q", view.Calls, view.Context)
	}
	if view.Ticket.ID != "T-08" || view.Ticket.Status != "open" || view.Ticket.Expect != nil {
		t.Errorf("тикет после хода: %+v", view.Ticket)
	}
	// Ход сам статус не меняет: это делает человек кнопкой.
	if tk, _ := st.Ticket("T-08"); tk.Status != "open" {
		t.Errorf("статус после ask: %s", tk.Status)
	}
}

func TestSupportAskEscalateAndErrors(t *testing.T) {
	srv, _ := supportServer(t, newMemStore(), fakeRun)
	resp, err := http.Post(srv.URL+"/api/support/tickets/T-11/ask", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	var view SupportView
	for _, e := range parseSSE(string(body)) {
		if e.name == "answer" {
			json.Unmarshal([]byte(e.data), &view)
		}
	}
	if view.Action != "escalate" || view.Reply.Clarify == "" || len(view.Sources) != 0 {
		t.Errorf("escalate: %+v", view)
	}
	if code := postJSON(t, srv.URL+"/api/support/tickets/T-99/ask", "{}", nil); code != http.StatusNotFound {
		t.Errorf("ask неизвестного: код %d", code)
	}

	// Ход упал без ответа — event: error.
	failing := func(ctx context.Context, id string, em agent.Emitter) (*SupportTurn, error) {
		return nil, errors.New("модель молчит")
	}
	srv2, _ := supportServer(t, newMemStore(), failing)
	resp, err = http.Post(srv2.URL+"/api/support/tickets/T-08/ask", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	gotErr := false
	for _, e := range parseSSE(string(body)) {
		if e.name == "error" && strings.Contains(e.data, "молчит") {
			gotErr = true
		}
	}
	if !gotErr {
		t.Errorf("нет event: error: %s", body)
	}

	// Без модели — 503, без CRM — 503 на всех маршрутах.
	srv3, _ := supportServer(t, newMemStore(), nil)
	if code := postJSON(t, srv3.URL+"/api/support/tickets/T-08/ask", "{}", nil); code != http.StatusServiceUnavailable {
		t.Errorf("без модели: код %d", code)
	}
	srv4, _ := supportServer(t, nil, nil)
	for _, u := range []string{"/api/support/tickets", "/api/support/tickets/T-08"} {
		if code := getJSON(t, srv4.URL+u, nil); code != http.StatusServiceUnavailable {
			t.Errorf("%s без CRM: код %d", u, code)
		}
	}
	if code := postJSON(t, srv4.URL+"/api/support/tickets/T-08/resolve", "{}", nil); code != http.StatusServiceUnavailable {
		t.Errorf("resolve без CRM: код %d", code)
	}
}

func TestSupportResolveAndEscalate(t *testing.T) {
	st := newMemStore()
	srv, _ := supportServer(t, st, fakeRun)

	var v TicketView
	if code := postJSON(t, srv.URL+"/api/support/tickets/T-08/resolve", `{"text":"Ключ не задан — добавь в .env","note":"см. aitr doctor"}`, &v); code != 200 {
		t.Fatalf("resolve: код %d", code)
	}
	if v.Ticket.Status != "resolved" || v.User == nil {
		t.Errorf("после resolve: %+v", v)
	}
	tk, _ := st.Ticket("T-08")
	kinds := []string{}
	for _, e := range tk.Events {
		kinds = append(kinds, e.Kind+":"+e.Text)
	}
	got := strings.Join(kinds, "|")
	if !strings.Contains(got, "status:Ключ не задан") || !strings.Contains(got, "note:см. aitr doctor") {
		t.Errorf("события: %s", got)
	}

	// Передать без тела — статус и текст по умолчанию.
	v = TicketView{}
	if code := postJSON(t, srv.URL+"/api/support/tickets/T-11/escalate", "", &v); code != 200 {
		t.Fatalf("escalate: код %d", code)
	}
	if v.Ticket.Status != "escalated" {
		t.Errorf("после escalate: %+v", v)
	}
	tk, _ = st.Ticket("T-11")
	if n := len(tk.Events); n != 1 || tk.Events[0].Kind != "status" || tk.Events[0].Text == "" {
		t.Errorf("события T-11: %+v", tk.Events)
	}

	var rows []TicketRow
	getJSON(t, srv.URL+"/api/support/tickets", &rows)
	if rows[0].Status != "resolved" || rows[1].Status != "escalated" {
		t.Errorf("список после кнопок: %+v", rows)
	}
	if code := postJSON(t, srv.URL+"/api/support/tickets/T-99/escalate", "{}", nil); code != http.StatusNotFound {
		t.Errorf("escalate неизвестного: код %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/support/tickets/T-08/resolve", "не json", nil); code != http.StatusBadRequest {
		t.Errorf("битое тело: код %d", code)
	}
}

func TestSupportPageSelectors(t *testing.T) {
	srv, _ := supportServer(t, nil, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{`data-tab="support"`, `id="support"`, `id="tickets"`, `id="ticket"`, `id="ask-btn"`, `id="resolve-btn"`, `id="escalate-btn"`, `id="escalate-note"`,
		`class="subject"`, `class="text"`, `class="user"`, `class="context"`, `class="attach"`, `/api/support/tickets`} {
		if !strings.Contains(html, want) {
			t.Errorf("на странице нет %s", want)
		}
	}
}
