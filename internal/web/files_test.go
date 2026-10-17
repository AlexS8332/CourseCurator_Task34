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

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/consent"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/llm/llmtest"
)

// fakeFiles — подставка файлового агента: план по цели с двумя файлами
// SKILL.md и docs/faq.md, согласие — consent.Decided по реплике, apply —
// write_file и git_commit в журнал, diff двух файлов, коммит.
type fakeFiles struct {
	mu   sync.Mutex
	runs []*FilesRun
	// planErr — этап plan не удался (прогон есть, с ошибкой).
	planErr error
}

func (f *fakeFiles) plan(ctx context.Context, goal string, em agent.Emitter) (*FilesRun, error) {
	em.Log(agent.Event{Kind: "llm.request", Step: 1})
	em.Log(agent.Event{Kind: "tool.call", Tool: "read_file", CallID: "call_1_read_file", Detail: `{"path":"scripts/aitr.ps1"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "read_file", CallID: "call_1_read_file", Title: "900 строк"})
	em.Log(agent.Event{Kind: "tool.call", Tool: "plan_done", CallID: "call_2_plan_done", Detail: `{"files":2}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "plan_done", CallID: "call_2_plan_done", Title: "принят"})
	f.mu.Lock()
	defer f.mu.Unlock()
	r := &FilesRun{ID: "20261017-120000-ab12cd34", Goal: goal, Stage: FilesStageApprove,
		Plan: &FilesPlan{Goal: goal, Writes: true, Steps: []string{"прочитать switch ($cmd) в scripts/aitr.ps1", "сверить таблицу «## Команды»", "дописать строки"},
			Files: []FilesPlanFile{{Path: ".claude/skills/aitr/SKILL.md", Why: "таблица команд"}, {Path: "docs/faq.md", Why: "раздел про команды"}, {Path: "docs/checks/cards.md", New: true, Why: "отчёт"}}},
		Stats: agent.Stats{Steps: 2, ToolCalls: 2},
		Calls: []help.Call{{Tool: "read_file", CallID: "call_1_read_file", Args: `{"path":"scripts/aitr.ps1"}`, Result: strings.Repeat("x", 3000)}}}
	if f.planErr != nil {
		r.Stage = FilesStagePlan
		r.Plan = nil
	}
	f.runs = append([]*FilesRun{r}, f.runs...)
	return r, f.planErr
}

func (f *fakeFiles) approve(ctx context.Context, id, reply string, em agent.Emitter) (*FilesRun, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var r *FilesRun
	for _, x := range f.runs {
		if x.ID == id {
			r = x
		}
	}
	if r == nil {
		return nil, false, errors.New("прогона нет: " + id)
	}
	if !consent.Decided(consent.Approve, reply) {
		return r, false, nil
	}
	r.Stage = FilesStageApply
	r.Branch, r.Worktree = "agent/"+r.ID, "sandbox/agent/"+r.ID
	em.Log(agent.Event{Kind: "mechanism", Title: "согласие: выданы write_file, apply_patch, git_commit — только файлы плана"})
	em.Log(agent.Event{Kind: "tool.call", Tool: "write_file", CallID: "call_3_write_file", Detail: `{"path":".claude/skills/aitr/SKILL.md"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "write_file", CallID: "call_3_write_file", Title: "записано"})
	em.Log(agent.Event{Kind: "tool.call", Tool: "write_file", CallID: "call_4_write_file", Detail: `{"path":"INDEX.md"}`})
	em.Log(agent.Event{Kind: "tool.error", Tool: "write_file", CallID: "call_4_write_file", Detail: "файла нет в плане — перепланируй", Rejected: true})
	em.Log(agent.Event{Kind: "tool.call", Tool: "git_commit", CallID: "call_5_git_commit", Detail: `{"message":"skill: таблица команд"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "git_commit", CallID: "call_5_git_commit", Title: "abc1234"})
	r.Stage = FilesStageDone
	r.Changed = []string{".claude/skills/aitr/SKILL.md", "docs/faq.md"}
	r.Commit = "abc1234"
	r.Summary = "таблица команд дополнена: record, card"
	r.Markdown = "## Файлы\n\nветка agent/" + r.ID
	r.Counters = FilesCounters{WritesOutsidePlan: 1}
	r.Stats = agent.Stats{Steps: 4, ToolCalls: 5, Rejected: 1}
	r.Diff = []DiffFile{
		{Path: ".claude/skills/aitr/SKILL.md", Status: "modified", Added: 1, Hunks: []DiffHunk{{Header: "@@ -40,2 +40,3 @@", OldStart: 40, OldLines: 2, NewStart: 40, NewLines: 3,
			Lines: []DiffLine{{Kind: "ctx", Old: 40, New: 40, Text: "| sync | пересобрать индекс |"}, {Kind: "add", New: 41, Text: "| record NN | контрольный прогон |"}}}}},
		{Path: "docs/faq.md", Status: "modified", Added: 1, Removed: 1, Hunks: []DiffHunk{{Header: "@@ -5,1 +5,1 @@", OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1,
			Lines: []DiffLine{{Kind: "del", Old: 5, Text: "девять команд"}, {Kind: "add", New: 5, Text: "десять команд"}}}}},
	}
	return r, true, nil
}

func (f *fakeFiles) list() []FilesRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []FilesRun{}
	for _, r := range f.runs {
		out = append(out, *r)
	}
	return out
}

func (f *fakeFiles) tab() *Files {
	return &Files{Plan: f.plan, Approve: f.approve, Runs: f.list, Gated: true}
}

func filesServer(t *testing.T, tab *Files) *httptest.Server {
	t.Helper()
	s := &Server{LLM: &llmtest.Fake{}, Model: "fake", Files: tab}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// filesStream — POST SSE: инструменты из журнала (tool.call), отказы
// (tool.error rejected), ответ (event: plan | report) и текст ошибки.
func filesStream(t *testing.T, srv *httptest.Server, path, body string) (map[string]int, *FilesView, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+path, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("content-type %q, код %d: %s", ct, resp.StatusCode, b)
	}
	raw, _ := io.ReadAll(resp.Body)
	calls := map[string]int{}
	var view *FilesView
	errText := ""
	for _, e := range parseSSE(string(raw)) {
		switch e.name {
		case "journal":
			var ev struct {
				Kind     string `json:"kind"`
				Tool     string `json:"tool"`
				Rejected bool   `json:"rejected"`
			}
			if err := json.Unmarshal([]byte(e.data), &ev); err != nil {
				t.Errorf("событие не JSON: %s", e.data)
			}
			if ev.Kind == "tool.call" {
				calls[ev.Tool]++
			}
			if ev.Kind == "tool.error" && ev.Rejected {
				calls["rejected:"+ev.Tool]++
			}
		case "plan", "report":
			view = &FilesView{}
			if err := json.Unmarshal([]byte(e.data), view); err != nil {
				t.Fatalf("ответ не разобран: %v: %s", err, e.data)
			}
			if view.View != e.name {
				t.Errorf("view %q в событии %s", view.View, e.name)
			}
		case "error":
			errText = e.data
		}
	}
	return calls, view, errText
}

func TestFilesPlanStreamsJournalAndPlan(t *testing.T) {
	fk := &fakeFiles{}
	srv := filesServer(t, fk.tab())
	calls, v, errText := filesStream(t, srv, "/api/files/plan", `{"goal":"обнови таблицу команд в SKILL.md по aitr.ps1"}`)
	if errText != "" || v == nil {
		t.Fatalf("плана нет: %s", errText)
	}
	if calls["read_file"] != 1 || calls["plan_done"] != 1 {
		t.Errorf("в журнале нет вызовов этапа plan: %v", calls)
	}
	if calls["write_file"] != 0 {
		t.Errorf("на этапе plan есть запись: %v", calls)
	}
	if v.ID == "" || v.Stage != FilesStageApprove || v.Plan == nil || len(v.Plan.Files) != 3 || !v.Plan.Writes {
		t.Errorf("план: %+v", v.FilesRun)
	}
	if v.Plan.Files[2].Path != "docs/checks/cards.md" || !v.Plan.Files[2].New {
		t.Errorf("новый файл плана: %+v", v.Plan.Files)
	}
	if len(v.Plan.Steps) != 3 {
		t.Errorf("шаги: %v", v.Plan.Steps)
	}
	// Права этапа: чтение — роль files, записи нет — «после согласия».
	if !v.Rights.Gated || v.Rights.Write != nil {
		t.Errorf("права на plan: %+v", v.Rights)
	}
	has := false
	for _, n := range v.Rights.Read {
		if n == "read_file" {
			has = true
		}
	}
	if !has {
		t.Errorf("в чтении нет read_file: %v", v.Rights.Read)
	}
	if v.Changed == nil || v.Diff == nil {
		t.Error("changed и diff должны быть списками, не null")
	}
	if len(v.Calls) != 1 || len(v.Calls[0].Result) > 2100 {
		t.Errorf("журнал вызовов не обрезан: %d", len(v.Calls))
	}
	if v.Seconds <= 0 {
		t.Error("нет времени хода")
	}
}

func TestFilesRightsJSON(t *testing.T) {
	f := &Files{Gated: true}
	b, _ := json.Marshal(f.Rights(FilesStageApprove))
	if !strings.Contains(string(b), `"write":"после согласия"`) || !strings.Contains(string(b), `"read_file"`) {
		t.Errorf("права plan при включённых правах: %s", b)
	}
	b, _ = json.Marshal(f.Rights(FilesStageDone))
	if !strings.Contains(string(b), `"write":["write_file","apply_patch","git_commit"]`) {
		t.Errorf("права после согласия: %s", b)
	}
	// Права выключены (контрольная дорожка): запись выдана уже на plan.
	off := &Files{Gated: false, ReadTools: []string{"read_file"}, WriteTools: []string{"write_file"}}
	b, _ = json.Marshal(off.Rights(FilesStageApprove))
	if string(b) != `{"read":["read_file"],"write":["write_file"]}` {
		t.Errorf("права без механизма: %s", b)
	}
	var back FilesRights
	if err := json.Unmarshal([]byte(`{"read":["a"],"write":"после согласия"}`), &back); err != nil || !back.Gated || back.Write != nil {
		t.Errorf("разбор строки write: %+v %v", back, err)
	}
	if err := json.Unmarshal(b, &back); err != nil || back.Gated || len(back.Write) != 1 {
		t.Errorf("разбор списка write: %+v %v", back, err)
	}
}

func TestFilesApproveRefusalKeepsStage(t *testing.T) {
	fk := &fakeFiles{}
	srv := filesServer(t, fk.tab())
	_, p, _ := filesStream(t, srv, "/api/files/plan", `{"goal":"цель"}`)
	if p == nil {
		t.Fatal("плана нет")
	}
	calls, v, errText := filesStream(t, srv, "/api/files/approve", `{"run_id":"`+p.ID+`","reply":"нет, не применяй — пересмотри план"}`)
	if errText != "" || v == nil {
		t.Fatalf("ответа нет: %s", errText)
	}
	if v.View != "plan" || v.Stage != FilesStageApprove || v.Note == "" || !strings.Contains(v.Note, "согласия") {
		t.Errorf("при отказе ждали plan без изменений с пометкой: view=%s stage=%s note=%q", v.View, v.Stage, v.Note)
	}
	if calls["write_file"] != 0 || v.Branch != "" || len(v.Changed) != 0 {
		t.Errorf("при отказе были правки: %v %+v", calls, v.FilesRun)
	}
	if !v.Rights.Gated {
		t.Errorf("права после отказа: %+v", v.Rights)
	}
}

func TestFilesApproveStreamsReport(t *testing.T) {
	fk := &fakeFiles{}
	srv := filesServer(t, fk.tab())
	_, p, _ := filesStream(t, srv, "/api/files/plan", `{"goal":"цель"}`)
	if p == nil {
		t.Fatal("плана нет")
	}
	calls, v, errText := filesStream(t, srv, "/api/files/approve", `{"run_id":"`+p.ID+`","reply":"да, применяй"}`)
	if errText != "" || v == nil {
		t.Fatalf("отчёта нет: %s", errText)
	}
	if v.View != "report" || v.Stage != FilesStageDone {
		t.Errorf("ждали report/done: %s/%s", v.View, v.Stage)
	}
	if calls["write_file"] != 2 || calls["git_commit"] != 1 || calls["rejected:write_file"] != 1 {
		t.Errorf("журнал apply: %v", calls)
	}
	if v.Branch != "agent/"+p.ID || v.Worktree == "" || v.Commit != "abc1234" {
		t.Errorf("ветка/коммит: %+v", v.FilesRun)
	}
	if len(v.Changed) != 2 || len(v.Diff) != 2 || v.Diff[0].Hunks[0].Lines[1].Kind != "add" {
		t.Errorf("изменённые и diff: %v %+v", v.Changed, v.Diff)
	}
	if v.Counters.WritesOutsidePlan != 1 || v.Counters.WritesBeforeApprove != 0 {
		t.Errorf("счётчики: %+v", v.Counters)
	}
	if v.Markdown == "" || v.Summary == "" {
		t.Error("нет отчёта markdown или сводки")
	}
	if v.Rights.Gated || len(v.Rights.Write) != 3 {
		t.Errorf("права после apply: %+v", v.Rights)
	}
	// Список прогонов: один, завершён, с веткой.
	var rows []FilesRow
	if code := getJSON(t, srv.URL+"/api/files/runs", &rows); code != http.StatusOK {
		t.Fatalf("runs: код %d", code)
	}
	if len(rows) != 1 || rows[0].ID != p.ID || rows[0].Stage != FilesStageDone || rows[0].Changed != 2 || rows[0].Branch == "" {
		t.Errorf("список прогонов: %+v", rows)
	}
}

func TestFilesPlanErrorKeepsRun(t *testing.T) {
	fk := &fakeFiles{planErr: errors.New("план не принят: путь вне разрешённых каталогов")}
	srv := filesServer(t, fk.tab())
	_, v, errText := filesStream(t, srv, "/api/files/plan", `{"goal":"цель"}`)
	if v == nil || errText != "" {
		t.Fatalf("прогон с ошибкой должен прийти событием plan: %s", errText)
	}
	if v.Error == "" || v.Stage != FilesStagePlan || v.Plan != nil {
		t.Errorf("ошибка этапа: %+v", v)
	}
}

func TestFilesErrors(t *testing.T) {
	// Вкладка не настроена — 503 на всех маршрутах.
	srv := filesServer(t, nil)
	var m map[string]string
	if code := postJSON(t, srv.URL+"/api/files/plan", `{"goal":"x"}`, &m); code != http.StatusServiceUnavailable {
		t.Errorf("без агента ждали 503, код %d", code)
	}
	if code := getJSON(t, srv.URL+"/api/files/runs", &m); code != http.StatusServiceUnavailable {
		t.Errorf("runs без агента: код %d", code)
	}
	// Без модели — план 503, список работает.
	srv = filesServer(t, &Files{Runs: func() []FilesRun { return nil }})
	if code := postJSON(t, srv.URL+"/api/files/plan", `{"goal":"x"}`, nil); code != http.StatusServiceUnavailable {
		t.Errorf("без модели: код %d", code)
	}
	var rows []FilesRow
	if code := getJSON(t, srv.URL+"/api/files/runs", &rows); code != http.StatusOK || len(rows) != 0 {
		t.Errorf("runs без модели: код %d %v", code, rows)
	}
	fk := &fakeFiles{}
	srv = filesServer(t, fk.tab())
	if code := postJSON(t, srv.URL+"/api/files/plan", `{"goal":"   "}`, &m); code != http.StatusBadRequest {
		t.Errorf("пустая цель: код %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/files/plan", `{"goal":`, &m); code != http.StatusBadRequest {
		t.Errorf("битое тело: код %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/files/approve", `{"reply":"да"}`, &m); code != http.StatusBadRequest {
		t.Errorf("без run_id: код %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/files/approve", `{"run_id":"r1","reply":"  "}`, &m); code != http.StatusBadRequest {
		t.Errorf("пустая реплика: код %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/files/approve", `{"run_id":"нет-такого","reply":"да"}`, &m); code != http.StatusNotFound {
		t.Errorf("чужой run_id: код %d", code)
	}
	// Runs не задан — поиск прогона на сервере, ошибка — событием error.
	srv = filesServer(t, &Files{Plan: fk.plan, Approve: fk.approve})
	_, v, errText := filesStream(t, srv, "/api/files/approve", `{"run_id":"нет-такого","reply":"да"}`)
	if v != nil || !strings.Contains(errText, "прогона нет") {
		t.Errorf("ждали event: error: %v %s", v, errText)
	}
}

func TestFilesPageSelectors(t *testing.T) {
	srv := filesServer(t, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{`data-tab="files"`, `id="files"`, `id="goal"`, `id="plan-btn"`, `id="plan"`, `id="rights"`, `id="reply"`, `id="approve"`, `id="report"`, `id="runs"`, `id="diff"`,
		`/api/files/plan`, `/api/files/approve`, `/api/files/runs`, `data-new`, `dataset.path`, `после согласия`, `"steps"`, `"branch"`, `"changed"`, `"counters"`} {
		if !strings.Contains(html, want) {
			t.Errorf("на странице нет %s", want)
		}
	}
	if strings.Count(html, `id="diff"`) != 1 || strings.Count(html, `id="approve"`) != 1 {
		t.Error("id=diff и id=approve должны быть по одному: вкладки делят узел")
	}
}
