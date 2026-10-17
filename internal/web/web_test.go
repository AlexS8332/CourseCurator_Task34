package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

func gitBranch() tools.Tool {
	return tools.Func{
		S:  tools.Spec{Name: "git_branch", Description: "ветка", Parameters: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`)},
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) { return `{"branch":"main"}`, nil },
	}
}

// scenario — модель зовёт git_branch и отвечает фактом о состоянии.
func scenario(req llm.Request) (llm.Response, error) {
	for _, m := range req.Messages {
		for _, c := range m.ToolCalls {
			if c.Function.Name == "git_branch" {
				a := help.Answer{Cited: cite.Cited{Status: "answered", Answer: "Корень на ветке main."},
					StateFacts: []help.StateFact{{Fact: "ветка main", CallID: c.ID}}}
				b, _ := json.Marshal(a)
				return llmtest.ToolCall(help.FinishName, string(b)), nil
			}
		}
	}
	return llmtest.ToolCall("git_branch", `{"repo":"root"}`), nil
}

// events — разбор потока SSE: имя события → данные по порядку.
type event struct{ name, data string }

func parseSSE(body string) []event {
	var out []event
	for _, block := range strings.Split(body, "\n\n") {
		var e event
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				e.name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				e.data = strings.TrimPrefix(line, "data: ")
			}
		}
		if e.name != "" {
			out = append(out, e)
		}
	}
	return out
}

func TestAskStreamsJournalAndAnswer(t *testing.T) {
	s := &Server{Deps: project.Deps{}, LLM: &llmtest.Fake{Fn: scenario}, Tools: []tools.Tool{gitBranch()}, Model: "fake"}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/ask", "application/json", strings.NewReader(`{"text":"/help на какой ветке корень?"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type %q", ct)
	}
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	evs := parseSSE(sb.String())
	journal, answers := 0, 0
	var view AnswerView
	toolCalls := map[string]bool{}
	for _, e := range evs {
		switch e.name {
		case "journal":
			journal++
			var ev struct {
				Kind string `json:"kind"`
				Tool string `json:"tool"`
			}
			if err := json.Unmarshal([]byte(e.data), &ev); err != nil {
				t.Errorf("событие не JSON: %s", e.data)
			}
			if ev.Kind == "tool.call" {
				toolCalls[ev.Tool] = true
			}
		case "answer":
			answers++
			if err := json.Unmarshal([]byte(e.data), &view); err != nil {
				t.Fatalf("ответ не разобран: %v: %s", err, e.data)
			}
		}
	}
	if journal == 0 || answers != 1 {
		t.Fatalf("журнал %d, ответов %d", journal, answers)
	}
	if !toolCalls["git_branch"] || !toolCalls[help.FinishName] {
		t.Errorf("в журнале нет вызовов: %v", toolCalls)
	}
	if view.Answer.Status != "answered" || view.Unverified || !strings.Contains(view.Text, "main") {
		t.Errorf("ответ: %+v", view)
	}
	if len(view.Calls) == 0 || view.Calls[0].Tool != "git_branch" {
		t.Errorf("вызовы: %+v", view.Calls)
	}
}

func TestPageAndState(t *testing.T) {
	s := &Server{Deps: project.Deps{}, LLM: &llmtest.Fake{}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body := make([]byte, 1<<20)
	n, _ := resp.Body.Read(body)
	resp.Body.Close()
	html := string(body[:n])
	for _, want := range []string{`id="status"`, `id="tabs"`, `id="log"`, `id="journal"`, `id="input"`, `id="form"`, `id="approve"`, `data-tab="help"`} {
		if !strings.Contains(html, want) {
			t.Errorf("на странице нет %s", want)
		}
	}
	if strings.Contains(html, "cdn.") || strings.Contains(html, "https://") {
		t.Error("страница тянет что-то извне")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'self'") {
		t.Errorf("CSP: %q", csp)
	}

	resp, err = http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st State
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if st.Note == "" || st.Model == "" {
		t.Errorf("без базы ожидалась заметка и модель: %+v", st)
	}
}

func TestAskRejectsEmpty(t *testing.T) {
	s := &Server{LLM: &llmtest.Fake{}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/ask", "application/json", strings.NewReader(`{"text":"  /help "}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("код %d", resp.StatusCode)
	}
	resp, err = http.Post(srv.URL+"/api/ask", "application/json", strings.NewReader(`не json`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("код %d", resp.StatusCode)
	}
}

func TestStateHook(t *testing.T) {
	s := &Server{LLM: &llmtest.Fake{}, State: func(ctx context.Context) (State, error) {
		return State{Index: IndexState{Docs: 40, Chunks: 900, Commit: "abcdef1"}, Branch: "main"}, nil
	}}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/state")
	if err != nil {
		t.Fatal(err)
	}
	var st State
	json.NewDecoder(resp.Body).Decode(&st)
	resp.Body.Close()
	if st.Index.Docs != 40 || st.Branch != "main" || st.Index.Commit != "abcdef1" {
		t.Errorf("%+v", st)
	}
}
