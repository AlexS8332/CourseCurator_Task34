package help

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

// Тесты идут на подставной модели (сценарий по запросу) и маленькой базе
// из двух документов курса, собранной в temp-каталоге с hash-эмбеддером.

const cardText = "Права этапа держатся отсутствием инструмента, а не абзацем промпта: инструмент перехода выдаётся только на своём этапе, " +
	"и модель не может перепрыгнуть через шаг, потому что ей нечем. Запись в журнале объясняет отказ."

func testDocs() []corpus.Doc {
	return []corpus.Doc{
		{Schema: 1, ID: "docs/cards/15-aitrenning-task15.md", Source: "card", Title: "15 — Жизненный цикл задачи",
			Intro: "Карточка задания 15: права этапа и запрет перепрыгивания.",
			Sections: []corpus.Section{
				{Path: []string{"Выводы"}, Title: "Выводы", Level: 2, Text: cardText},
			}},
		{Schema: 1, ID: "README.md", Source: "readme", Title: "AITrenning — рабочий каталог курса",
			Intro: "Здесь живут задания курса и всё, что вокруг них: конспекты, карточки заданий, шаблоны сред и скрипт быстрого доступа.",
			Sections: []corpus.Section{
				{Path: []string{"Команды"}, Title: "Команды", Level: 2, Text: "Стеки шаблонов: go, python, node, prompt. Команда aitr doctor проверяет среду задания."},
			}},
	}
}

// deps — база в temp-каталоге и поиск по ней.
func deps(t *testing.T) project.Deps {
	t.Helper()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "course.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.PutCorpus(ctx, testDocs(), corpus.Manifest{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	return project.Deps{Store: st, Searcher: &kb.Searcher{Store: st, Embedder: embed.Hash{}}, Index: string(kb.Structure)}
}

// gitBranch — подставной инструмент состояния.
func gitBranch() tools.Tool {
	return tools.Func{
		S: tools.Spec{Name: "git_branch", Description: "ветка репозитория", Parameters: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`)},
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			return `{"repo":"root","branch":"main","dirty":false}`, nil
		},
	}
}

func assignmentsList() tools.Tool {
	return tools.Func{
		S: tools.Spec{Name: "assignments_list", Description: "задания", Parameters: json.RawMessage(`{"type":"object","properties":{}}`)},
		Fn: func(ctx context.Context, args json.RawMessage) (string, error) {
			return `{"count":31,"items":[{"num":"30","status":"done"},{"num":"31","status":"in-progress"}]}`, nil
		},
	}
}

// hitWith — фрагмент выдачи docs_search из истории запроса с подстрокой.
func hitWith(t *testing.T, req llm.Request, sub string) cite.SearchHit {
	t.Helper()
	for _, m := range req.Messages {
		if m.Role != llm.RoleTool || !strings.Contains(m.Content, `"hits"`) {
			continue
		}
		content, _ := tools.Unwrap(m.Content)
		var out cite.SearchResult
		if err := json.Unmarshal([]byte(content), &out); err != nil {
			t.Fatalf("ответ docs_search не разобран: %v: %s", err, m.Content)
		}
		for _, h := range out.Hits {
			if strings.Contains(h.Text, sub) {
				return h
			}
		}
		t.Fatalf("в выдаче docs_search нет фрагмента с %q", sub)
	}
	t.Fatal("в запросе нет ответа docs_search")
	return cite.SearchHit{}
}

// calledTool — звала ли модель инструмент в истории запроса.
func calledTool(req llm.Request, name string) bool {
	for _, m := range req.Messages {
		for _, c := range m.ToolCalls {
			if c.Function.Name == name {
				return true
			}
		}
	}
	return false
}

func finish(a Answer) llm.Response {
	b, _ := json.Marshal(a)
	return llmtest.ToolCall(FinishName, string(b))
}

func TestDocsAnswerVerbatim(t *testing.T) {
	d := deps(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if !llmtest.HasTool(req, DocsTool) {
			t.Error("у агента нет docs_search")
		}
		if llmtest.ToolReplies(req) != 1 {
			t.Errorf("вызов docs_search кодом: ответов инструментов %d, ожидался 1", llmtest.ToolReplies(req))
		}
		h := hitWith(t, req, "Права этапа держатся")
		quote := string([]rune(cardText)[:60])
		return finish(Answer{Cited: cite.Cited{Status: "answered",
			Answer:  "Права этапа появились в пятнадцатом задании и держатся отсутствием инструмента.",
			Sources: []cite.CitedSource{{ChunkID: h.ChunkID}},
			Quotes:  []cite.CitedQuote{{ChunkID: h.ChunkID, Text: quote}}}}), nil
	}}
	rec := &agent.Recorder{}
	turn, err := Run(context.Background(), fake, d, "/help в каком задании появились права этапа?", rec, Options{Tools: []tools.Tool{gitBranch()}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Answer.Unknown() || !turn.Check.OK || turn.Check.Verbatim != 1 || !turn.Check.HasSources {
		t.Fatalf("ответ не принят как проверенный: %+v", turn.Check)
	}
	if turn.Question != "в каком задании появились права этапа?" {
		t.Errorf("вопрос не очищен от /help: %q", turn.Question)
	}
	if fake.Calls() != 1 {
		t.Errorf("запросов к модели %d, ожидался 1", fake.Calls())
	}
	if len(turn.Hits) == 0 || !strings.Contains(turn.Text, "15") {
		t.Errorf("в итоге нет выдачи или заголовка: %q", turn.Text)
	}
	if calls := turn.Calls.Of(DocsTool); len(calls) != 1 || calls[0].Result == "" {
		t.Errorf("журнал вызовов: %+v", calls)
	}
}

func TestStateFactFromTool(t *testing.T) {
	d := deps(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		switch llmtest.ToolReplies(req) {
		case 1: // после docs_search кодом
			return llmtest.ToolCall("git_branch", `{"repo":"root"}`), nil
		}
		return finish(Answer{Cited: cite.Cited{Status: "answered", Answer: "Корень курса на ветке main."},
			StateFacts: []StateFact{{Fact: "ветка main", CallID: "call_0_git_branch"}}}), nil
	}}
	turn, err := Run(context.Background(), fake, d, "на какой ветке корень?", nil, Options{Tools: []tools.Tool{gitBranch(), assignmentsList()}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Answer.Unknown() || turn.StateUnverified || len(turn.StateProblems) != 0 {
		t.Fatalf("факт из инструмента не принят: %+v %v", turn.Answer, turn.StateProblems)
	}
	if turn.Stats.Rejected != 0 || fake.Calls() != 2 {
		t.Errorf("отказов %d, запросов %d", turn.Stats.Rejected, fake.Calls())
	}
	if sc := turn.Calls.StateCalls(); len(sc) != 1 || sc[0].Tool != "git_branch" {
		t.Errorf("вызовы состояния: %+v", sc)
	}
}

func TestStateFactRejectedThenFixed(t *testing.T) {
	d := deps(t)
	var rejection string
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		last := llmtest.LastToolReply(req)
		switch {
		case llmtest.ToolReplies(req) == 1:
			// Ветка названа без инструмента — должен быть отказ.
			return finish(Answer{Cited: cite.Cited{Status: "answered", Answer: "Корень на ветке main, заданий 31."}}), nil
		case strings.Contains(last, `"error"`) && !calledTool(req, "git_branch"):
			rejection = last
			return llmtest.ToolCalls(llmtest.Call{Name: "git_branch", Args: `{"repo":"root"}`}, llmtest.Call{Name: "assignments_list", Args: `{}`}), nil
		}
		return finish(Answer{Cited: cite.Cited{Status: "answered", Answer: "Корень на ветке main, заданий 31."},
			StateFacts: []StateFact{{Fact: "ветка main", CallID: "call_0_git_branch"}, {Fact: "заданий 31", CallID: "call_1_assignments_list"}}}), nil
	}}
	turn, err := Run(context.Background(), fake, d, "на какой ветке корень и сколько заданий?", nil, Options{Tools: []tools.Tool{gitBranch(), assignmentsList()}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Stats.Rejected != 1 {
		t.Errorf("отказов %d, ожидался 1", turn.Stats.Rejected)
	}
	if !strings.Contains(rejection, "ветка «main»") || !strings.Contains(rejection, "git_branch") {
		t.Errorf("отказ не объясняет, что сделать: %s", rejection)
	}
	if turn.StateUnverified || turn.Answer.Unknown() {
		t.Fatalf("исправленный ответ не принят: %+v", turn)
	}
}

func TestStateUnverifiedAfterRejects(t *testing.T) {
	d := deps(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return finish(Answer{Cited: cite.Cited{Status: "answered", Answer: "Корень на ветке develop."}}), nil
	}}
	turn, err := Run(context.Background(), fake, d, "на какой ветке корень?", nil, Options{Tools: []tools.Tool{gitBranch()}})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.StateUnverified || turn.Stats.Rejected != cite.MaxRejects {
		t.Fatalf("после %d отказов ответ должен быть принят с пометкой: unverified=%v rejected=%d", cite.MaxRejects, turn.StateUnverified, turn.Stats.Rejected)
	}
	if !strings.Contains(turn.Text, "не проверены") {
		t.Errorf("в тексте нет пометки: %q", turn.Text)
	}
}

func TestUnknownOutOfCourse(t *testing.T) {
	d := deps(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return finish(Answer{Cited: cite.Cited{Status: "unknown", Answer: "В документации курса нет сведений о весе манула.", Clarify: "Вопрос о каком-то задании курса?"}}), nil
	}}
	turn, err := Run(context.Background(), fake, d, "сколько весит манул?", nil, Options{Tools: []tools.Tool{gitBranch()}})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Answer.Unknown() || turn.Answer.Clarify == "" || turn.Unverified() {
		t.Fatalf("ожидался unknown с уточнением: %+v", turn.Answer)
	}
}

func TestNoRAGLane(t *testing.T) {
	d := deps(t)
	fs := features.Catalog().Defaults().With(RAG, false)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if llmtest.HasTool(req, DocsTool) {
			t.Error("при выключенном help.rag docs_search выдан")
		}
		if llmtest.ToolReplies(req) != 0 {
			t.Error("вызов кодом при выключенном help.rag")
		}
		return finish(Answer{Cited: cite.Cited{Status: "answered", Answer: "Права этапа появились в задании пятнадцать."}}), nil
	}}
	turn, err := Run(context.Background(), fake, d, "в каком задании появились права этапа?", nil, Options{Features: fs, Tools: []tools.Tool{gitBranch()}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Answer.Unknown() || turn.Check.HasSources || turn.Features.On(RAG) {
		t.Fatalf("без базы ответ принимается без источников: %+v", turn)
	}
}

func TestNoToolsLane(t *testing.T) {
	d := deps(t)
	fs := features.Catalog().Defaults().With(Tools, false)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if llmtest.HasTool(req, "git_branch") {
			t.Error("при выключенном help.tools выдан git_branch")
		}
		return finish(Answer{Cited: cite.Cited{Status: "unknown", Answer: "Инструментов состояния нет.", Clarify: "Проверить ветку командой git branch?"}}), nil
	}}
	turn, err := Run(context.Background(), fake, d, "на какой ветке корень?", nil, Options{Features: fs, Tools: []tools.Tool{gitBranch()}})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Answer.Unknown() {
		t.Fatalf("%+v", turn.Answer)
	}
}

func TestNoBaseFallsBack(t *testing.T) {
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return finish(Answer{Cited: cite.Cited{Status: "answered", Answer: "Ответ без базы."}}), nil
	}}
	rec := &agent.Recorder{}
	turn, err := Run(context.Background(), fake, project.Deps{}, "вопрос", rec, Options{Tools: []tools.Tool{}})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Features.On(RAG) {
		t.Error("без базы help.rag должен быть выключен в наборе хода")
	}
	found := false
	for _, e := range rec.Events {
		if e.Kind == agent.EventMechanism && strings.Contains(e.Title, "откат") {
			found = true
		}
	}
	if !found {
		t.Error("в журнале нет события отката")
	}
}

func TestProtocolErrorGivesUnverifiedUnknown(t *testing.T) {
	d := deps(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return llmtest.Text("Отвечаю текстом."), nil
	}}
	turn, err := Run(context.Background(), fake, d, "что такое edgerec?", nil, Options{Tools: []tools.Tool{}})
	if err == nil {
		t.Fatal("ожидалась ошибка протокола")
	}
	if turn == nil || !turn.Answer.Unknown() || !turn.Check.Unverified {
		t.Fatalf("ожидался unknown с пометкой «не проверено»: %+v", turn)
	}
}

func TestSpecDefaults(t *testing.T) {
	d := deps(t)
	obs := &cite.Issued{}
	spec := SpecWith(d, obs, &CallLog{}, "q", Options{Tools: []tools.Tool{gitBranch()}})
	if spec.Name != Name || spec.MaxSteps != MaxSteps || len(spec.Finish) != 1 || spec.Finish[0].Name != FinishName {
		t.Fatalf("%+v", spec)
	}
	names := tools.Names(spec.Tools)
	if strings.Join(names, ",") != "git_branch,docs_search" {
		t.Errorf("инструменты: %v", names)
	}
}

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"/help вопрос": "вопрос", "  вопрос ": "вопрос", "/help": "", "/helpless": "/helpless", "/Help  x": "x",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMentions(t *testing.T) {
	ms := Mentions("Корень на ветке `feature/x-1`, задание 31 в статусе in-progress; отчёт в demos/31-course-help/runs/1/report.json, проверок 14.")
	got := map[string]string{}
	for _, m := range ms {
		got[m.Kind+":"+m.Text] = ""
	}
	for _, want := range []string{"branch:feature/x-1", "status:in-progress", "path:demos/31-course-help/runs/1/report.json", "number:31", "number:14"} {
		if _, ok := got[want]; !ok {
			t.Errorf("нет упоминания %s среди %v", want, ms)
		}
	}
	if _, ok := got["number:1"]; ok {
		t.Error("число внутри пути посчитано отдельно")
	}
}

func TestContainsNumberToken(t *testing.T) {
	if contains("заданий 30", Mention{Kind: "number", Text: "3"}) {
		t.Error("«3» нашлось в «30»")
	}
	if !contains("всего 3 задания", Mention{Kind: "number", Text: "3"}) {
		t.Error("«3» не нашлось")
	}
	if !contains("branch: Main", Mention{Kind: "branch", Text: "main"}) {
		t.Error("ветка без учёта регистра не нашлась")
	}
}

func TestCallLog(t *testing.T) {
	l := &CallLog{}
	l.Log(agent.Event{Kind: agent.EventToolCall, Tool: "git_branch", CallID: "c1", Detail: `{"repo":"root"}`, Step: 1})
	l.Log(agent.Event{Kind: agent.EventToolResult, Tool: "git_branch", CallID: "c1", Detail: `{"branch":"main"}`, Seconds: 0.2})
	l.Log(agent.Event{Kind: agent.EventToolCall, Tool: FinishName, CallID: "c2", Final: true})
	l.Log(agent.Event{Kind: agent.EventToolError, Tool: FinishName, CallID: "c2", Final: true, Rejected: true, Detail: "ответ не принят"})
	l.Log(agent.Event{Kind: agent.EventToolResult, CallID: "nope"})
	c, ok := l.Get("c1")
	if !ok || c.Result != `{"branch":"main"}` || c.Secs != 0.2 || c.Error {
		t.Fatalf("%+v %v", c, ok)
	}
	if sc := l.StateCalls(); len(sc) != 1 {
		t.Errorf("вызовы состояния: %+v", sc)
	}
	if f, _ := l.Get("c2"); !f.Final || !f.Error {
		t.Errorf("завершающий: %+v", f)
	}
}
