package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

func TestCheckLanes(t *testing.T) {
	reg := features.Catalog()
	base := reg.Defaults()
	mech, err := CheckLanes(reg, []Lane{
		{Name: "main", Features: base},
		{Name: "norag", Features: base.With(help.RAG, false)},
		{Name: "same", Features: base, Same: true},
	})
	if err != nil || mech != help.RAG {
		t.Fatalf("mech %q err %v", mech, err)
	}
	// Две дорожки с разными механизмами — не стенд.
	_, err = CheckLanes(reg, []Lane{
		{Name: "main", Features: base},
		{Name: "norag", Features: base.With(help.RAG, false)},
		{Name: "notools", Features: base.With(help.Tools, false)},
	})
	if err == nil {
		t.Fatal("ожидалась ошибка: дорожки сравнивают разные механизмы")
	}
	// Повтор без Same — ошибка.
	if _, err := CheckLanes(reg, []Lane{{Name: "a", Features: base}, {Name: "b", Features: base}}); err == nil {
		t.Fatal("ожидалась ошибка: повтор без Same")
	}
}

func TestMarkdownAndWrite(t *testing.T) {
	r := &Result{Trial: Trial14, Stand: "docs", Mechanism: help.RAG, Lanes: []Lane{{Name: "main"}, {Name: "same", Same: true}},
		Checks:  []Check{{What: "must", Lane: "main", OK: true, Got: "10/10", Need: "≥ 9/10"}, {What: "x", Lane: "main", OK: false, Got: "1", Need: "0"}},
		Metrics: []Metric{{Name: "цена", Lane: "main", Value: 0.1234, Unit: "$"}},
		Rows:    []Row{{Lane: "main", ID: "T01", Question: "а | б", Status: "answered", OK: true, Answer: "ответ"}}}
	md := Markdown([]*Result{r})
	for _, want := range []string{"НЕ пройдено", "| ✓ | must", "| ✗ | x", "0.12 $", "а ¦ б"} {
		if !strings.Contains(md, want) {
			t.Errorf("в отчёте нет %q:\n%s", want, md)
		}
	}
	dir := t.TempDir()
	mdPath, jsPath, err := Write(dir, "t14", []*Result{r})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(mdPath); err != nil {
		t.Error(err)
	}
	data, _ := os.ReadFile(jsPath)
	var back []Result
	if err := json.Unmarshal(data, &back); err != nil || len(back) != 1 || back[0].Stand != "docs" {
		t.Errorf("json: %v %+v", err, back)
	}
}

func TestMustOKAndNumbers(t *testing.T) {
	e := &kb.Expect{Must: [][]string{{"отсутствием инструмента", "нет инструмента"}, {"задании 15", "задание 15"}}, MustNot: []string{"ошибка"}}
	if !MustOK("Права этапа появились в задании 15 и держатся отсутствием инструмента.", e) {
		t.Error("must не нашёлся")
	}
	if MustOK("Права этапа держатся отсутствием инструмента.", e) {
		t.Error("пропущена вторая группа")
	}
	if MustOK("В задании 15 нет инструмента — ошибка.", e) {
		t.Error("must_not не сработал")
	}
	if hasNumber("заданий 30", "3") || !hasNumber("заданий 30", "30") || !hasNumber("задание 07", "7") {
		t.Error("hasNumber")
	}
}

// --- И-14 на подставной модели -------------------------------------------

const cardText = "Права этапа держатся отсутствием инструмента, а не абзацем промпта: инструмент перехода выдаётся только на своём этапе, " +
	"и модель не может перепрыгнуть через шаг, потому что ей нечем."

const readmeText = "Стеки шаблонов: go, python, node, prompt. Команда aitr doctor проверяет среду задания: метаданные, git, тулчейн, ключи."

func testDeps(t *testing.T) project.Deps {
	t.Helper()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "course.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	docs := []corpus.Doc{
		{Schema: 1, ID: "docs/cards/15-aitrenning-task15.md", Source: "card", Title: "15 — Жизненный цикл задачи", Intro: "Карточка задания 15.",
			Sections: []corpus.Section{{Path: []string{"Выводы"}, Title: "Выводы", Level: 2, Text: cardText}}},
		{Schema: 1, ID: "README.md", Source: "readme", Title: "AITrenning", Intro: "Рабочий каталог курса.",
			Sections: []corpus.Section{{Path: []string{"Команды"}, Title: "Команды", Level: 2, Text: readmeText}}},
	}
	if err := st.PutCorpus(ctx, docs, corpus.Manifest{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	return project.Deps{Store: st, Searcher: &kb.Searcher{Store: st, Embedder: embed.Hash{}}, Index: string(kb.Structure)}
}

func fakeTools() []tools.Tool {
	return []tools.Tool{
		tools.Func{S: tools.Spec{Name: "git_branch", Description: "ветка", Parameters: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`)},
			Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
				return `{"repo":"root","branch":"main"}`, nil
			}},
		tools.Func{S: tools.Spec{Name: "assignments_list", Description: "задания", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
			Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
				return `{"count":31,"items":[{"num":"30","status":"done"},{"num":"31","status":"in-progress"}]}`, nil
			}},
	}
}

const questionsJSON = `{"schema":1,"version":1,"questions":[
{"id":"T01","split":"test","type":"docs","q":"чем держатся права этапа?","answerable":true,"expect":{"must":[["отсутствием инструмента"]]},"evidence":[{"doc_id":"docs/cards/15-aitrenning-task15.md","quote":"держатся отсутствием инструмента"}]},
{"id":"T02","split":"test","type":"docs","q":"какие стеки шаблонов есть?","answerable":true,"expect":{"must":[["go"],["prompt"]]},"evidence":[{"doc_id":"README.md","quote":"Стеки шаблонов: go, python, node, prompt"}]},
{"id":"D01","split":"dev","type":"docs","q":"что проверяет aitr doctor?","answerable":true,"expect":{"must":[["тулчейн"]]},"evidence":[{"doc_id":"README.md","quote":"проверяет среду задания"}]},
{"id":"S01","split":"test","type":"state","q":"на какой ветке корень курса?","answerable":true,"note":"ref:branch"},
{"id":"S02","split":"test","type":"state","q":"сколько всего заданий в каталоге?","answerable":true,"note":"ref:count"},
{"id":"O01","split":"out","type":"out","q":"сколько весит манул?","answerable":false}
]}`

func firstUser(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			return m.Content
		}
	}
	return ""
}

func firstHit(req llm.Request) (cite.SearchHit, bool) {
	for _, m := range req.Messages {
		if m.Role != llm.RoleTool || !strings.Contains(m.Content, `"hits"`) {
			continue
		}
		content, _ := tools.Unwrap(m.Content)
		var out cite.SearchResult
		if json.Unmarshal([]byte(content), &out) == nil && len(out.Hits) > 0 {
			return out.Hits[0], true
		}
	}
	return cite.SearchHit{}, false
}

func called(req llm.Request, name string) bool {
	for _, m := range req.Messages {
		for _, c := range m.ToolCalls {
			if c.Function.Name == name {
				return true
			}
		}
	}
	return false
}

func finish(a help.Answer) llm.Response {
	b, _ := json.Marshal(a)
	return llmtest.ToolCall(help.FinishName, string(b))
}

// scenario — подставная модель, которая ведёт себя «правильно», когда у неё
// есть инструменты, и честно хуже — когда их нет.
func scenario(req llm.Request) (llm.Response, error) {
	q := firstUser(req)
	switch {
	case strings.Contains(q, "манул"):
		return finish(help.Answer{Cited: cite.Cited{Status: "unknown", Answer: "В документации курса о манулах ничего нет.", Clarify: "Речь о каком-то задании курса?"}}), nil
	case strings.Contains(q, "ветке"):
		if !llmtest.HasTool(req, "git_branch") {
			return finish(help.Answer{Cited: cite.Cited{Status: "unknown", Answer: "Инструментов состояния нет.", Clarify: "Проверить git branch вручную?"}}), nil
		}
		if !called(req, "git_branch") {
			return llmtest.ToolCall("git_branch", `{"repo":"root"}`), nil
		}
		return finish(help.Answer{Cited: cite.Cited{Status: "answered", Answer: "Корень курса на ветке main."},
			StateFacts: []help.StateFact{{Fact: "ветка main", CallID: "call_0_git_branch"}}}), nil
	case strings.Contains(q, "сколько всего заданий"):
		if !llmtest.HasTool(req, "assignments_list") {
			return finish(help.Answer{Cited: cite.Cited{Status: "unknown", Answer: "Инструментов состояния нет.", Clarify: "Посчитать по assignments/?"}}), nil
		}
		if !called(req, "assignments_list") {
			return llmtest.ToolCall("assignments_list", `{}`), nil
		}
		return finish(help.Answer{Cited: cite.Cited{Status: "answered", Answer: "В каталоге 31 задание."},
			StateFacts: []help.StateFact{{Fact: "заданий 31", CallID: "call_0_assignments_list"}}}), nil
	}
	h, ok := firstHit(req)
	if !ok {
		// Без базы модель сначала зовёт какой-нибудь инструмент (лишний шаг — так ведёт себя
		// и настоящая), потом отвечает по памяти, без ключевых слов набора.
		if llmtest.HasTool(req, "git_branch") && !called(req, "git_branch") {
			return llmtest.ToolCall("git_branch", `{"repo":"root"}`), nil
		}
		return finish(help.Answer{Cited: cite.Cited{Status: "answered", Answer: "Точно не помню, это описано в карточках."}}), nil
	}
	quote := string([]rune(h.Text)[:min(60, len([]rune(h.Text)))])
	return finish(help.Answer{Cited: cite.Cited{Status: "answered", Answer: h.Text,
		Sources: []cite.CitedSource{{ChunkID: h.ChunkID}}, Quotes: []cite.CitedQuote{{ChunkID: h.ChunkID, Text: quote}}}}), nil
}

func TestT14Fake(t *testing.T) {
	d := testDeps(t)
	qpath := filepath.Join(t.TempDir(), "help.json")
	if err := os.WriteFile(qpath, []byte(questionsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := &llmtest.Fake{Fn: scenario}
	ref := &Reference{Branch: "main", NotDone: []string{"31"}, Count: 31, LastRun: map[string]RunRef{}}
	var log strings.Builder
	results, err := T14(context.Background(), T14Options{Deps: d, LLM: fake, Model: "fake", Questions: qpath, Ref: ref, Tools: fakeTools(), Log: &log, Judge: true})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if len(results) != 2 {
		t.Fatalf("стендов %d", len(results))
	}
	for _, r := range results {
		for _, c := range r.Checks {
			if !c.OK {
				t.Errorf("стенд %s: проверка «%s» (%s) не прошла: %s, нужно %s", r.Stand, c.What, c.Lane, c.Got, c.Need)
			}
		}
		if r.Requests == 0 || len(r.Rows) == 0 || r.Mechanism == "" {
			t.Errorf("стенд %s пуст: %+v", r.Stand, r)
		}
	}
	docs := results[0]
	if docs.Mechanism != help.RAG || results[1].Mechanism != help.Tools {
		t.Errorf("механизмы: %s %s", docs.Mechanism, results[1].Mechanism)
	}
	if !strings.Contains(Markdown(results), "судья-модель") {
		t.Error("нет заметки о судье")
	}
	// Без базы must на test-вопросах — 0 (ответ «не помню»).
	for _, m := range docs.Metrics {
		if m.Name == "must (test)" && m.Lane == "norag" && m.Value != 0 {
			t.Errorf("must без базы = %v", m.Value)
		}
	}
	md, js, err := Write(t.TempDir(), "t14", results)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(md); err != nil {
		t.Error(err)
	}
	if _, err := os.Stat(js); err != nil {
		t.Error(err)
	}
}

func TestEqualRef(t *testing.T) {
	ref := Reference{Branch: "main", NotDone: []string{"29", "31"}, Count: 31, LastRun: map[string]RunRef{"30": {Has: true, Passed: true, Checks: 14, ChecksOK: 14}}}
	turn := func(ans string, facts ...help.StateFact) *help.Turn {
		return &help.Turn{Answer: help.Answer{Cited: cite.Cited{Status: "answered", Answer: ans}, StateFacts: facts}, Calls: &help.CallLog{}}
	}
	q := func(note string) kb.Question { return kb.Question{Note: note} }
	if ok, why := EqualRef(turn("ветка main"), q("ref:branch"), ref); !ok {
		t.Error(why)
	}
	if ok, _ := EqualRef(turn("ветка develop"), q("ref:branch"), ref); ok {
		t.Error("чужая ветка принята")
	}
	if ok, why := EqualRef(turn("не done: 29 и 31"), q("ref:not-done"), ref); !ok {
		t.Error(why)
	}
	if ok, _ := EqualRef(turn("не done: 29"), q("ref:not-done"), ref); ok {
		t.Error("неполный список принят")
	}
	if ok, why := EqualRef(turn("всего 31 задание"), q("ref:count"), ref); !ok {
		t.Error(why)
	}
	if ok, why := EqualRef(turn("последний прогон пройден: проверок 14 из 14"), q("ref:last-run:30"), ref); !ok {
		t.Error(why)
	}
	if ok, _ := EqualRef(turn("последний прогон не пройден: 14/14"), q("ref:last-run:30"), ref); ok {
		t.Error("противоречие с passed принято")
	}
	if ok, _ := EqualRef(turn("прогон"), q("ref:last-run:29"), Reference{LastRun: map[string]RunRef{}}); ok {
		t.Error("прогона нет, а ответ его описывает")
	}
	if ok, _ := EqualRef(&help.Turn{Answer: help.Answer{Cited: cite.Cited{Status: "unknown"}}}, q("ref:last-run:29"), Reference{LastRun: map[string]RunRef{}}); !ok {
		t.Error("unknown при отсутствии прогона — верный ответ")
	}
}
