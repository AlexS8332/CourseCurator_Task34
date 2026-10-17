package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm/llmtest"
)

// plantedFixture — подставные PR по образцу eval/review/prs.json.
func plantedFixture() ([]PlantedRow, error) {
	return []PlantedRow{
		{ID: "P1", Title: "edgerec: отчёт без проверки ошибки записи", Kind: "bug", File: "tools/edgerec/report.go", Line: 40, Note: "_ = os.WriteFile"},
		{ID: "P4", Title: "scripts/publish.ps1: публикация с force-push", Kind: "rule", File: "scripts/publish.ps1", Line: 12, Note: "git push --force и печать ключа"},
		{ID: "P5", Title: "опечатка в конспекте", Clean: true, File: "docs/notes/README.md", Line: 3},
	}, nil
}

// p4Diff — diff подставного PR P4: новый файл scripts/publish.ps1.
func p4Diff() []DiffFile {
	lines := []DiffLine{}
	src := []string{"param([string]$Num)", "", "# Публикация задания", "$dir = Join-Path 'assignments' $Num", "Set-Location $dir",
		"git add -A", "git commit -m 'publish'", "", "# Отправка", "git push --force origin main", "", "Write-Host \"ключ: $env:DEEPSEEK_API_KEY\""}
	for i, t := range src {
		lines = append(lines, DiffLine{Kind: "add", New: i + 1, Text: t})
	}
	return []DiffFile{{Path: "scripts/publish.ps1", Status: "added", Added: len(src),
		Hunks: []DiffHunk{{Header: "@@ -0,0 +1,12 @@", NewStart: 1, NewLines: len(src), Lines: lines}}}}
}

// fakeReview — ход ревью на подставке: в журнал — docs_search кодом и
// read_file; P4 — находка rule с цитатой CLAUDE.md на строке 10 (посаженная
// 12), P5 — без находок, иначе — bug.
func fakeReview(ctx context.Context, req ReviewRequest, em agent.Emitter) (*ReviewTurn, error) {
	em.Log(agent.Event{Kind: "tool.call", Tool: "docs_search", CallID: "call_0_docs_search", Title: "документация: вызов docs_search кодом до первого запроса", Detail: `{"q":"push ключ PowerShell"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "docs_search", CallID: "call_0_docs_search", Title: "фрагментов 3"})
	em.Log(agent.Event{Kind: "llm.request", Step: 1})
	em.Log(agent.Event{Kind: "tool.call", Tool: "read_file", CallID: "call_1_read_file", Detail: `{"path":"scripts/publish.ps1"}`})
	em.Log(agent.Event{Kind: "tool.result", Tool: "read_file", CallID: "call_1_read_file", Title: "12 строк"})
	calls := []help.Call{
		{Tool: "docs_search", CallID: "call_0_docs_search", Args: `{"q":"push ключ PowerShell"}`, Result: "фрагментов 3"},
		{Tool: "read_file", CallID: "call_1_read_file", Args: `{"path":"scripts/publish.ps1"}`, Result: strings.Repeat("x", 3000)},
	}
	chunk := kb.Chunk{ID: "CLAUDE.md/structure/003", DocID: "CLAUDE.md", Title: "Правила работы в каталоге курса", Section: "Коммиты и публикация", Path: []string{"Коммиты и публикация"},
		Text: "Коммиты создавай сам, а git push (и тем более force-push) — только с явного разрешения пользователя."}
	stats := agent.Stats{Steps: 2, ToolCalls: 2}
	switch req.Planted {
	case "P4":
		return &ReviewTurn{
			Input: ReviewInput{Title: "scripts/publish.ps1: публикация с force-push", Files: p4Diff()},
			Findings: []ReviewFinding{
				{File: "scripts/publish.ps1", Line: 10, Kind: "rule", Severity: "major", Text: "force-push без разрешения пользователя",
					Evidence: ReviewEvidence{ChunkID: chunk.ID, Quote: "git push (и тем более force-push) — только с явного разрешения пользователя"}},
				{File: "scripts/publish.ps1", Line: 12, Kind: "recommendation", Severity: "minor", Text: "ключ в выводе"},
			},
			Summary: "нужны правки: публикация нарушает правила проекта", Verdict: "changes",
			Markdown: "## Ревью\n\n**Вердикт: нужны правки**", Check: cite.CiteCheck{OK: true, HasSources: true, HasQuotes: true, Verbatim: 1},
			Stats: stats, Calls: calls, Hits: []kb.Hit{{Chunk: chunk, Score: 0.9, Rank: 1}},
		}, nil
	case "P5":
		return &ReviewTurn{Input: ReviewInput{Title: "опечатка в конспекте", Files: []DiffFile{{Path: "docs/notes/README.md", Status: "modified", Added: 1, Removed: 1,
			Hunks: []DiffHunk{{Header: "@@ -2,3 +2,3 @@", OldStart: 2, OldLines: 3, NewStart: 2, NewLines: 3, Lines: []DiffLine{
				{Kind: "ctx", Old: 2, New: 2, Text: ""}, {Kind: "del", Old: 3, Text: "конспекты по темам"}, {Kind: "add", New: 3, Text: "Конспекты по темам"}, {Kind: "ctx", Old: 4, New: 4, Text: ""}}}}}}},
			Summary: "замечаний нет", Markdown: "## Ревью\n\nзамечаний нет", Check: cite.CiteCheck{OK: true}, Stats: stats, Calls: calls}, nil
	}
	// Текущая ветка: вердикт не задан — окно выводит его из находок.
	return &ReviewTurn{Input: ReviewInput{Title: "ветка", Range: req.Range, Files: []DiffFile{{Path: "tools/edgerec/report.go", Status: "modified"}}},
		Findings: []ReviewFinding{{File: "tools/edgerec/report.go", Line: 41, Kind: "bug", Severity: "major", Text: "ошибка записи проигнорирована",
			Evidence: ReviewEvidence{Path: "tools/edgerec/report.go", Line: 41}}},
		Summary: "одна ошибка", Check: cite.CiteCheck{OK: true}, Stats: stats, Calls: calls}, nil
}

func reviewServer(t *testing.T, rv *Review) *httptest.Server {
	t.Helper()
	s := &Server{LLM: &llmtest.Fake{}, Model: "fake", Review: rv}
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// runReview — POST /api/review/run: инструменты из журнала и ответ.
func runReview(t *testing.T, srv *httptest.Server, body string) (map[string]bool, *ReviewView, string) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/review/run", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("content-type %q, код %d: %s", ct, resp.StatusCode, b)
	}
	raw, _ := io.ReadAll(resp.Body)
	tools := map[string]bool{}
	var view *ReviewView
	errText := ""
	for _, e := range parseSSE(string(raw)) {
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
			view = &ReviewView{}
			if err := json.Unmarshal([]byte(e.data), view); err != nil {
				t.Fatalf("ответ не разобран: %v: %s", err, e.data)
			}
		case "error":
			errText = e.data
		}
	}
	return tools, view, errText
}

func TestReviewPlantedList(t *testing.T) {
	srv := reviewServer(t, &Review{Planted: plantedFixture, Range: "main...feature"})
	var rows []PlantedRow
	if code := getJSON(t, srv.URL+"/api/review/planted", &rows); code != 200 {
		t.Fatalf("список: код %d", code)
	}
	if len(rows) != 4 || rows[0].ID != "P1" || rows[0].Kind != "bug" || rows[2].ID != "P5" || !rows[2].Clean {
		t.Errorf("список: %+v", rows)
	}
	last := rows[len(rows)-1]
	if last.ID != BranchID || last.Range != "main...feature" || last.Title == "" {
		t.Errorf("текущая ветка: %+v", last)
	}
	// Без подставных — одна «текущая ветка» с диапазоном по умолчанию.
	srv2 := reviewServer(t, &Review{})
	rows = nil
	getJSON(t, srv2.URL+"/api/review/planted", &rows)
	if len(rows) != 1 || rows[0].Range != DefaultRange {
		t.Errorf("без подставных: %+v", rows)
	}
	// Список не загрузился — 500 с причиной.
	srv3 := reviewServer(t, &Review{Planted: func() ([]PlantedRow, error) { return nil, errors.New("prs.json: нет файла") }})
	if code := getJSON(t, srv3.URL+"/api/review/planted", nil); code != http.StatusInternalServerError {
		t.Errorf("битый список: код %d", code)
	}
}

func TestReviewRunPlantedFound(t *testing.T) {
	srv := reviewServer(t, &Review{Planted: plantedFixture, Run: fakeReview})
	tools, v, errText := runReview(t, srv, `{"planted":"P4"}`)
	if v == nil {
		t.Fatalf("ответа нет: %s", errText)
	}
	if !tools["docs_search"] || !tools["read_file"] {
		t.Errorf("вызовы в журнале: %v", tools)
	}
	if v.Verdict != "changes" || len(v.Findings) != 2 || v.Unverified || v.Error != "" {
		t.Errorf("ответ: %+v", v)
	}
	if len(v.Input.Files) != 1 || v.Input.Files[0].Path != "scripts/publish.ps1" || len(v.Input.Files[0].Hunks[0].Lines) != 12 || v.Input.Files[0].Hunks[0].Lines[9].Kind != "add" {
		t.Errorf("diff: %+v", v.Input.Files)
	}
	f := v.Findings[0]
	if f.Kind != "rule" || f.Line != 10 || !strings.Contains(f.Evidence.Quote, "force-push") {
		t.Errorf("находка: %+v", f)
	}
	// Заголовок фрагмента — из выдачи хода.
	if f.Evidence.Title != "Правила работы в каталоге курса" || f.Evidence.Section != "Коммиты и публикация" {
		t.Errorf("основание: %+v", f.Evidence)
	}
	// Посаженная строка 12, находка на 10 (|d| ≤ 3, вид rule) — найдено.
	if v.Planted == nil || !v.Planted.Found || v.Planted.ID != "P4" || v.Planted.Line != 12 || v.Planted.Finding == nil || v.Planted.Finding.Line != 10 {
		t.Errorf("посаженное: %+v", v.Planted)
	}
	if len(v.Calls) != 2 || len(v.Calls[1].Result) > 2100 {
		t.Errorf("вызовы: %d, результат %d байт", len(v.Calls), len(v.Calls[1].Result))
	}
	if !strings.Contains(v.Markdown, "Вердикт") || v.Summary == "" {
		t.Errorf("markdown %q, summary %q", v.Markdown, v.Summary)
	}
}

func TestReviewRunCleanAndBranch(t *testing.T) {
	srv := reviewServer(t, &Review{Planted: plantedFixture, Run: fakeReview, Range: "main...HEAD"})
	_, v, errText := runReview(t, srv, `{"planted":"P5"}`)
	if v == nil {
		t.Fatalf("ответа нет: %s", errText)
	}
	if v.Verdict != "ok" || len(v.Findings) != 0 || v.Findings == nil {
		t.Errorf("чистый: %+v", v)
	}
	if v.Planted == nil || !v.Planted.Clean || !v.Planted.Found {
		t.Errorf("посаженное у чистого: %+v", v.Planted)
	}
	// Текущая ветка: вердикт выводится из находок, посаженного нет.
	_, v, errText = runReview(t, srv, `{"range":"main...HEAD"}`)
	if v == nil {
		t.Fatalf("ответа нет: %s", errText)
	}
	if v.Verdict != "changes" || v.Planted != nil || v.Input.Range != "main...HEAD" {
		t.Errorf("ветка: verdict %q planted %+v range %q", v.Verdict, v.Planted, v.Input.Range)
	}
	// Пустое тело и id «branch» — тоже текущая ветка с диапазоном сервера.
	for _, body := range []string{"", `{"planted":"branch"}`} {
		_, v, _ = runReview(t, srv, body)
		if v == nil || v.Input.Range != "main...HEAD" {
			t.Errorf("тело %q: %+v", body, v)
		}
	}
}

func TestReviewRunErrors(t *testing.T) {
	srv := reviewServer(t, &Review{Planted: plantedFixture, Run: fakeReview})
	if code := postJSON(t, srv.URL+"/api/review/run", `{"planted":"P9"}`, nil); code != http.StatusNotFound {
		t.Errorf("неизвестный PR: код %d", code)
	}
	if code := postJSON(t, srv.URL+"/api/review/run", "не json", nil); code != http.StatusBadRequest {
		t.Errorf("битое тело: код %d", code)
	}
	// Ход упал без ответа — event: error.
	failing := &Review{Planted: plantedFixture, Run: func(ctx context.Context, req ReviewRequest, em agent.Emitter) (*ReviewTurn, error) {
		return nil, errors.New("модель молчит")
	}}
	_, v, errText := runReview(t, reviewServer(t, failing), `{"planted":"P4"}`)
	if v != nil || !strings.Contains(errText, "молчит") {
		t.Errorf("ошибка хода: view %+v, err %q", v, errText)
	}
	// Ход с ошибкой, но с ответом — ответ с полем error.
	partial := &Review{Planted: plantedFixture, Run: func(ctx context.Context, req ReviewRequest, em agent.Emitter) (*ReviewTurn, error) {
		t, _ := fakeReview(ctx, req, em)
		t.Unverified = true
		return t, errors.New("принят после отказов")
	}}
	_, v, _ = runReview(t, reviewServer(t, partial), `{"planted":"P4"}`)
	if v == nil || !strings.Contains(v.Error, "отказов") || !v.Unverified {
		t.Errorf("частичный ответ: %+v", v)
	}
	// Без модели — 503; без вкладки — 503 на списке и ходе, 404 на панели.
	srv2 := reviewServer(t, &Review{Planted: plantedFixture})
	if code := postJSON(t, srv2.URL+"/api/review/run", `{"planted":"P4"}`, nil); code != http.StatusServiceUnavailable {
		t.Errorf("без модели: код %d", code)
	}
	srv3 := reviewServer(t, nil)
	if code := getJSON(t, srv3.URL+"/api/review/planted", nil); code != http.StatusServiceUnavailable {
		t.Errorf("список без вкладки: код %d", code)
	}
	if code := postJSON(t, srv3.URL+"/api/review/run", `{}`, nil); code != http.StatusServiceUnavailable {
		t.Errorf("ход без вкладки: код %d", code)
	}
	if code := getJSON(t, srv3.URL+"/api/review/ci", nil); code != http.StatusNotFound {
		t.Errorf("панель без вкладки: код %d", code)
	}
}

func TestPlantedResultRule(t *testing.T) {
	p := PlantedRow{ID: "P2", File: "scripts/aitr.ps1", Line: 100, Kind: "rule"}
	f := func(file string, line int, kind string) ReviewFinding {
		return ReviewFinding{File: file, Line: line, Kind: kind}
	}
	cases := []struct {
		name  string
		list  []ReviewFinding
		found bool
		line  int
	}{
		{"точно", []ReviewFinding{f("scripts/aitr.ps1", 100, "rule")}, true, 100},
		{"в допуске", []ReviewFinding{f("./scripts\\aitr.ps1", 103, "rule")}, true, 103},
		{"далеко", []ReviewFinding{f("scripts/aitr.ps1", 104, "rule")}, false, 0},
		{"другой вид", []ReviewFinding{f("scripts/aitr.ps1", 100, "bug")}, false, 0},
		{"другой файл", []ReviewFinding{f("scripts/other.ps1", 100, "rule")}, false, 0},
		{"ближайшая", []ReviewFinding{f("scripts/aitr.ps1", 97, "rule"), f("scripts/aitr.ps1", 101, "rule")}, true, 101},
		{"пусто", nil, false, 0},
	}
	for _, c := range cases {
		r := plantedResult(p, c.list)
		if r.Found != c.found || (c.found && r.Finding.Line != c.line) {
			t.Errorf("%s: %+v", c.name, r)
		}
	}
	clean := PlantedRow{ID: "P5", Clean: true}
	if r := plantedResult(clean, nil); !r.Found || !r.Clean {
		t.Errorf("чистый без находок: %+v", r)
	}
	if r := plantedResult(clean, []ReviewFinding{f("docs/notes/README.md", 3, "bug")}); r.Found {
		t.Errorf("чистый с находкой: %+v", r)
	}
}

func TestReviewCI(t *testing.T) {
	dir := t.TempDir()
	// Нет файлов — 404.
	srv := reviewServer(t, &Review{ExamplesDir: dir})
	if code := getJSON(t, srv.URL+"/api/review/ci", nil); code != http.StatusNotFound {
		t.Errorf("пустой каталог: код %d", code)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// Объект с полями и журнал gh api (массив ревью) — берётся больший номер.
	write("ci-3.json", `{"number":3,"verdict":"ok","url":"https://github.com/x/y/pull/3","at":"2026-10-16T10:00:00Z","findings":[]}`)
	write("ci-12.json", `[{"id":1,"body":"старое","html_url":"https://github.com/x/y/pull/12#r1","submitted_at":"2026-10-16T11:00:00Z"},
		{"id":2,"body":"<!-- curator-review:abc -->\n**Вердикт: нужны правки**","html_url":"https://github.com/x/y/pull/12#r2","submitted_at":"2026-10-16T12:00:00Z","pull_request_url":"https://api.github.com/repos/x/y/pulls/12"}]`)
	var run CIRun
	if code := getJSON(t, srv.URL+"/api/review/ci", &run); code != 200 {
		t.Fatalf("панель: код %d", code)
	}
	if run.Number != 12 || run.Verdict != "changes" || !strings.HasSuffix(run.URL, "#r2") || run.At != "2026-10-16T12:00:00Z" || run.File != "ci-12.json" {
		t.Errorf("последний прогон: %+v", run)
	}
	// Объект: поля как есть.
	run = ParseCI("ci-3.json", []byte(`{"number":3,"verdict":"ok","url":"u","at":"t","findings":[{"file":"a"}],"title":"PR"}`))
	if run.Number != 3 || run.Verdict != "ok" || run.URL != "u" || run.At != "t" || run.Findings != 1 || run.Title != "PR" {
		t.Errorf("объект: %+v", run)
	}
	// Битый JSON — только номер из имени.
	run = ParseCI("ci-7.json", []byte("{"))
	if run.Number != 7 || run.Verdict != "" {
		t.Errorf("битый: %+v", run)
	}
	// Вердикт по тексту.
	run = ParseCI("ci-8.json", []byte(`{"markdown":"## Ревью\n\nзамечаний нет"}`))
	if run.Verdict != "ok" {
		t.Errorf("по тексту: %+v", run)
	}
}

func TestReviewPageSelectors(t *testing.T) {
	srv := reviewServer(t, nil)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	html := string(body)
	for _, want := range []string{`data-tab="review"`, `id="review"`, `id="prs"`, `id="review-btn"`, `id="diff"`, `id="findings"`, `id="verdict"`, `id="planted"`, `id="summary"`, `id="ci"`,
		`/api/review/planted`, `/api/review/run`, `/api/review/ci`, `data-verdict`, `data-severity`, `flagged`, `замечаний нет`, `нужны правки`, `посаженное найдено`} {
		if !strings.Contains(html, want) {
			t.Errorf("на странице нет %s", want)
		}
	}
}
