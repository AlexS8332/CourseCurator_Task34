package support

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/ps"
	"aitr/34-files-agent/internal/tools"
)

// Тесты идут без сети, Python и PowerShell: модель — llmtest.Fake по
// сценарию, aitr — ps.Fake, корень курса — временный каталог с двумя
// файлами-признаками и одним заданием, CRM — временный каталог с
// users.json и tickets.json, база — два документа с hash-эмбеддером.

const (
	faqKeys = "Ключи лежат в .env корня курса или в .env.local задания. Команда aitr doctor NN печатает по каждому ключу из assignment.json " +
		"«задан» или «не задан»; значения ключей не печатаются. Если DEEPSEEK_API_KEY не задан — добавь строку DEEPSEEK_API_KEY=... в .env корня и повтори aitr doctor."
	faqTests = "Тесты задания запускает aitr test NN: для стека go это go vet ./... и go test ./..., для стека python — uv run pytest в каталоге задания. " +
		"Для go test -race нужен gcc из mingw-w64 в PATH: системный gcc из msys2 — cygwin-сборка, и -race с ним не собирается."
)

const doctorOut = "doctor: 31-course-help\n\nМетаданные\n  ok    stack = go\n  ok    status = done\n\nGit\n  ok    репозиторий инициализирован\n" +
	"  warn  origin не настроен\n  ok    автор коммитов: AlexS8332\n  ok    рабочее дерево чистое\n\nТулчейн\n  ok    go version go1.25.0 windows/amd64\n" +
	"  ok    go.mod на месте\n\nКлючи\n  ok    корневой .env: ключей 3\n  FAIL  DEEPSEEK_API_KEY - не задан\n\nДокументация\n  ok    карточка: docs/cards/31-course-help.md\n"

const reportJSON = `{"title":"Куратор","passed":false,"checks":3,"checks_ok":2,"video_seconds":40,"frames":100,` +
	`"steps":[{"n":1,"action":"goto","target":"/","status":"ok"},{"n":2,"action":"click","target":"#ask","status":"ok"},` +
	`{"n":3,"action":"expect","target":".msg.assistant","caption":"Ответ","status":"fail","error":"элемент не появился за 10s"}],` +
	`"js_errors":["Failed to load resource: 401"]}`

// testRoot — временный корень курса с заданием 31 и одним прогоном.
func testRoot(t *testing.T) *course.Root {
	t.Helper()
	dir := t.TempDir()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte("# правила"), 0o644))
	must(os.MkdirAll(filepath.Join(dir, "scripts"), 0o755))
	must(os.WriteFile(filepath.Join(dir, "scripts", "aitr.ps1"), []byte("# aitr"), 0o644))
	a := filepath.Join(dir, "assignments", "31-course-help")
	must(os.MkdirAll(a, 0o755))
	must(os.WriteFile(filepath.Join(a, "assignment.json"),
		[]byte(`{"num":"31","slug":"course-help","title":"Куратор курса","stack":"go","status":"in-progress","repo":"","env":["DEEPSEEK_API_KEY"]}`), 0o644))
	run := filepath.Join(dir, "demos", "31-course-help", "runs", "20261016-120000")
	must(os.MkdirAll(run, 0o755))
	must(os.WriteFile(filepath.Join(run, "report.json"), []byte(reportJSON), 0o644))
	root, err := course.Open(dir)
	must(err)
	return root
}

// testStore — временная CRM с тремя тикетами: T-08 (ключ не задан),
// T-10 (близнец python) и T-11 (вне корпуса).
func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	users := `[
{"id":"u-agent-31","name":"Claude Code в задании 31","role":"agent","os":"Windows 10","shell":"PowerShell 5.1","tools":{"go":true,"uv":true,"gh":true,"node":false},"assignments":["31"]},
{"id":"u-peer","name":"Дмитрий","role":"peer","os":"macOS","shell":"zsh","tools":{"go":true,"uv":true,"gh":false},"assignments":[]}]`
	tickets := `[
{"id":"T-08","user":"u-agent-31","num":"31","stack":"go","subject":"Почему не работает авторизация?","text":"При запуске curator help ответ: нужен DEEPSEEK_API_KEY. Почему не работает авторизация?","status":"open",
 "attach":"curator help: нужен DEEPSEEK_API_KEY — в окружении или в .env/.env.local корня курса","expect":{"must":[["не задан"]],"sources":["docs/faq.md"],"action":"resolve"}},
{"id":"T-10","user":"u-peer","stack":"python","subject":"Тесты не запускаются","text":"тесты не запускаются, что делать?","status":"open","expect":{"must":[["pytest","uv run"]],"must_not":["go test","go vet"],"action":"resolve"}},
{"id":"T-11","user":"u-peer","subject":"Kubernetes","text":"Как развернуть задание в Kubernetes?","status":"open","expect":{"action":"escalate"}}]`
	if err := os.WriteFile(filepath.Join(dir, UsersFile), []byte(users), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, TicketsFile), []byte(tickets), 0o644); err != nil {
		t.Fatal(err)
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// deps — корень, база из двух документов и подставной aitr.
func deps(t *testing.T) project.Deps {
	t.Helper()
	ResetDoctorCache()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "course.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	docs := []corpus.Doc{
		{Schema: 1, ID: "docs/faq.md", Source: "doc", Title: "FAQ оснастки", Intro: "Вопросы и ответы об aitr и edgerec.",
			Sections: []corpus.Section{
				{Path: []string{"Ключи"}, Title: "Ключи", Level: 2, Text: faqKeys},
				{Path: []string{"Тесты"}, Title: "Тесты", Level: 2, Text: faqTests},
			}},
	}
	if err := st.PutCorpus(ctx, docs, corpus.Manifest{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	return project.Deps{Root: testRoot(t), Store: st, Searcher: &kb.Searcher{Store: st, Embedder: embed.Hash{}},
		Index: string(kb.Structure), PS: ps.Fake{"doctor 31": doctorOut}}
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
			t.Fatalf("ответ docs_search не разобран: %v", err)
		}
		for _, h := range out.Hits {
			if strings.Contains(h.Text, sub) {
				return h
			}
		}
	}
	t.Fatalf("в выдаче docs_search нет фрагмента с %q", sub)
	return cite.SearchHit{}
}

// systemBlock — системное сообщение запроса (блок механизма), которое
// начинается с prefix; системный промпт не в счёт.
func systemBlock(req llm.Request, prefix string) (string, bool) {
	for _, m := range req.Messages {
		if m.Role == llm.RoleSystem && strings.HasPrefix(m.Content, prefix) {
			return m.Content, true
		}
	}
	return "", false
}

func finish(r Reply) llm.Response {
	b, _ := json.Marshal(r)
	return llmtest.ToolCall(FinishName, string(b))
}

func quoteOf(text string, n int) string {
	r := []rune(text)
	return string(r[:min(n, len(r))])
}

func TestResolveVerbatimClosesTicket(t *testing.T) {
	d, s := deps(t), testStore(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		for _, n := range []string{DocsTool, CRMTicket, CRMUser, CRMSearch, CRMNote, project.AssignmentGet, project.DemoReport, project.AitrDoctor} {
			if !llmtest.HasTool(req, n) {
				t.Errorf("у агента нет инструмента %s", n)
			}
		}
		if llmtest.ToolReplies(req) != 1 {
			t.Errorf("вызов docs_search кодом: ответов инструментов %d, ожидался 1", llmtest.ToolReplies(req))
		}
		block, ok := systemBlock(req, BlockTitle)
		if !ok || !strings.Contains(block, "DEEPSEEK_API_KEY - не задан") || !strings.Contains(block, "агент Claude Code") {
			t.Errorf("блок контекста не дошёл до модели: %q", block)
		}
		if strings.Contains(req.Messages[len(req.Messages)-1].Content, "агент Claude Code") {
			t.Error("реплика пользователя несёт данные контекста")
		}
		h := hitWith(t, req, "не задан")
		return finish(Reply{Cited: cite.Cited{
			Answer:  "Ключ DEEPSEEK_API_KEY не задан — это видно по aitr doctor. Добавьте строку с ключом в .env корня курса и повторите aitr doctor. Источник: FAQ оснастки, раздел «Ключи».",
			Sources: []cite.CitedSource{{ChunkID: h.ChunkID}},
			Quotes:  []cite.CitedQuote{{ChunkID: h.ChunkID, Text: "Если DEEPSEEK_API_KEY не задан — добавь строку DEEPSEEK_API_KEY=... в .env корня и повтори aitr doctor"}},
		}, Action: ActionResolve, Note: "ключ не задан по doctor; дан путь к .env"}), nil
	}}
	rec := &agent.Recorder{}
	turn, err := Run(context.Background(), fake, d, s, "T-08", rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Check.OK || turn.Check.Unverified || turn.Check.Verbatim != 1 || !turn.Check.HasSources {
		t.Fatalf("ответ не принят как проверенный: %+v", turn.Check)
	}
	if turn.Applied != StatusResolved || fake.Calls() != 1 || turn.Stats.Rejected != 0 {
		t.Fatalf("applied %q, запросов %d, отказов %d", turn.Applied, fake.Calls(), turn.Stats.Rejected)
	}
	tk, _ := s.Ticket("T-08")
	if tk.Status != StatusResolved || s.StatusCalls() != 1 {
		t.Fatalf("статус %s, вызовов SetStatus %d", tk.Status, s.StatusCalls())
	}
	kinds := map[string]int{}
	for _, e := range tk.Events {
		kinds[e.Kind]++
	}
	if kinds[EventStatus] != 1 || kinds[EventNote] != 1 {
		t.Errorf("события: %+v", tk.Events)
	}
	if !strings.Contains(turn.Text, "FAQ оснастки") || !strings.Contains(turn.Text, "Тикет закрыт") {
		t.Errorf("текст: %q", turn.Text)
	}
	if !strings.Contains(turn.Context.Doctor, "не задан") || turn.Context.Assignment == nil || turn.Context.LastRun == nil {
		t.Errorf("контекст не собран: %+v", turn.Context)
	}
	if calls := turn.Calls.Of(DocsTool); len(calls) != 1 {
		t.Errorf("журнал вызовов: %+v", calls)
	}
	// Записанный tickets.json читается заново с тем же статусом.
	again, err := Open(s.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if tk2, _ := again.Ticket("T-08"); tk2.Status != StatusResolved {
		t.Errorf("после перечитывания статус %s", tk2.Status)
	}
}

func TestEscalateOutOfCorpus(t *testing.T) {
	d, s := deps(t), testStore(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return finish(Reply{Cited: cite.Cited{Answer: "В документации оснастки о Kubernetes ничего нет.",
			Clarify: "Речь о каком-то задании курса или о внешнем проекте?"}, Action: ActionEscalate}), nil
	}}
	turn, err := Run(context.Background(), fake, d, s, "T-11", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Reply.Action != ActionEscalate || !turn.Check.OK || turn.Unverified() || turn.Applied != StatusEscalated {
		t.Fatalf("%+v %+v", turn.Reply, turn.Check)
	}
	tk, _ := s.Ticket("T-11")
	if tk.Status != StatusEscalated || !strings.Contains(tk.Events[len(tk.Events)-1].Text, "Речь о каком-то") {
		t.Errorf("тикет: %+v", tk)
	}
	if !strings.Contains(turn.Text, "Уточните:") || !strings.Contains(turn.Text, "передан") {
		t.Errorf("текст: %q", turn.Text)
	}
}

func TestMustNotRejectedThenFixed(t *testing.T) {
	d, s := deps(t), testStore(t)
	var rejection string
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		h := hitWith(t, req, "uv run pytest")
		quote := "для стека python — uv run pytest в каталоге задания"
		last := llmtest.LastToolReply(req)
		if strings.Contains(last, `"error"`) {
			rejection = last
			return finish(Reply{Cited: cite.Cited{Answer: "Запустите uv run pytest в каталоге задания — так тесты python запускает aitr test (FAQ, раздел «Тесты»).",
				Sources: []cite.CitedSource{{ChunkID: h.ChunkID}}, Quotes: []cite.CitedQuote{{ChunkID: h.ChunkID, Text: quote}}}, Action: ActionResolve}), nil
		}
		return finish(Reply{Cited: cite.Cited{Answer: "Запустите go vet ./... и go test ./... — так тесты запускает aitr test.",
			Sources: []cite.CitedSource{{ChunkID: h.ChunkID}}, Quotes: []cite.CitedQuote{{ChunkID: h.ChunkID, Text: quote}}}, Action: ActionResolve}), nil
	}}
	turn, err := Run(context.Background(), fake, d, s, "T-10", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Stats.Rejected != 1 || !strings.Contains(rejection, "«go vet»") || !strings.Contains(rejection, "python") {
		t.Fatalf("отказов %d, отказ: %s", turn.Stats.Rejected, rejection)
	}
	if !turn.Check.OK || turn.Applied != StatusResolved {
		t.Fatalf("исправленный ответ не принят: %+v applied %q", turn.Check, turn.Applied)
	}
}

func TestChunkIDAndSecretRejected(t *testing.T) {
	d, s := deps(t), testStore(t)
	var rejection string
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		h := hitWith(t, req, "не задан")
		quote := "Команда aitr doctor NN печатает по каждому ключу из assignment.json «задан» или «не задан»"
		cited := cite.Cited{Sources: []cite.CitedSource{{ChunkID: h.ChunkID}}, Quotes: []cite.CitedQuote{{ChunkID: h.ChunkID, Text: quote}}}
		if last := llmtest.LastToolReply(req); strings.Contains(last, `"error"`) {
			rejection = last
			cited.Answer = "Ключ DEEPSEEK_API_KEY не задан: aitr doctor печатает по каждому ключу «задан» или «не задан»."
			cited.Clarify = "Ключ уже есть в .env корня?"
			return finish(Reply{Cited: cited, Action: ActionReply}), nil
		}
		cited.Answer = "Ключ не задан (см. " + h.ChunkID + "); задайте DEEPSEEK_API_KEY=sk-abcdef0123456789."
		return finish(Reply{Cited: cited, Action: ActionReply}), nil
	}}
	turn, err := Run(context.Background(), fake, d, s, "T-08", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Stats.Rejected != 1 || !strings.Contains(rejection, "chunk_id") || !strings.Contains(rejection, "ключа") {
		t.Fatalf("отказов %d, отказ: %s", turn.Stats.Rejected, rejection)
	}
	if !turn.Check.OK || turn.Applied != ActionReply || !strings.Contains(turn.Text, "Уточните:") {
		t.Fatalf("reply не принят: %+v %q", turn.Check, turn.Applied)
	}
	tk, _ := s.Ticket("T-08")
	if tk.Status != StatusOpen || s.StatusCalls() != 0 || tk.Events[len(tk.Events)-1].Kind != EventReply {
		t.Errorf("после reply: статус %s, SetStatus %d, события %+v", tk.Status, s.StatusCalls(), tk.Events)
	}
}

func TestResolveWithoutQuotesUnverifiedKeepsOpen(t *testing.T) {
	d, s := deps(t), testStore(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return finish(Reply{Cited: cite.Cited{Answer: "Ключ не задан, добавьте его в .env."}, Action: ActionResolve}), nil
	}}
	turn, err := Run(context.Background(), fake, d, s, "T-08", nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Unverified() || turn.Stats.Rejected != cite.MaxRejects {
		t.Fatalf("после %d отказов ответ принимается с пометкой: %+v", cite.MaxRejects, turn.Check)
	}
	if turn.Applied != ActionReply || s.StatusCalls() != 0 {
		t.Fatalf("непроверенный resolve не должен закрывать тикет: applied %q, SetStatus %d", turn.Applied, s.StatusCalls())
	}
	if tk, _ := s.Ticket("T-08"); tk.Status != StatusOpen {
		t.Errorf("статус %s", tk.Status)
	}
	if !strings.Contains(turn.Text, "Не проверено") {
		t.Errorf("в тексте нет пометки: %q", turn.Text)
	}
}

func TestNoContextLane(t *testing.T) {
	d, s := deps(t), testStore(t)
	fs := features.Catalog().Defaults().With(Mechanism, false)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if _, ok := systemBlock(req, BlockTitle); ok {
			t.Error("при выключенном support.context блок контекста в запросе")
		}
		if !llmtest.HasTool(req, CRMTicket) || !llmtest.HasTool(req, project.AitrDoctor) {
			t.Error("без контекста инструменты CRM и aitr_doctor должны остаться")
		}
		if llmtest.ToolReplies(req) == 1 {
			return llmtest.ToolCall(project.AitrDoctor, `{"num":"31"}`), nil
		}
		if !strings.Contains(llmtest.LastToolReply(req), "не задан") {
			t.Errorf("aitr_doctor не вернул строки ключей: %s", llmtest.LastToolReply(req))
		}
		h := hitWith(t, req, "не задан")
		return finish(Reply{Cited: cite.Cited{Answer: "Ключ DEEPSEEK_API_KEY не задан по aitr doctor; добавьте его в .env корня и повторите aitr doctor.",
			Sources: []cite.CitedSource{{ChunkID: h.ChunkID}},
			Quotes:  []cite.CitedQuote{{ChunkID: h.ChunkID, Text: "добавь строку DEEPSEEK_API_KEY=... в .env корня и повтори aitr doctor"}}}, Action: ActionResolve}), nil
	}}
	turn, err := Run(context.Background(), fake, d, s, "T-08", nil, Options{Features: fs})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Features.On(Mechanism) || fake.Calls() != 2 || turn.Applied != StatusResolved {
		t.Fatalf("features %s, запросов %d, applied %q", turn.Features, fake.Calls(), turn.Applied)
	}
	if sc := turn.Calls.StateCalls(); len(sc) != 1 || sc[0].Tool != project.AitrDoctor {
		t.Errorf("вызовы состояния: %+v", sc)
	}
}

func TestGateForcesEscalate(t *testing.T) {
	// База пуста по теме: hash-эмбеддер что-то найдёт всегда, поэтому
	// выдача отсекается другим путём — финишёр без наблюдателя с пустой
	// выдачей.
	f := Finisher(&cite.Issued{}, Ticket{ID: "T-11", Text: "Kubernetes"})
	args := func(r Reply) json.RawMessage { b, _ := json.Marshal(r); return b }
	answered := Reply{Cited: cite.Cited{Answer: "Разверните через helm."}, Action: ActionResolve}
	for i := 0; i < cite.MaxRejects; i++ {
		if _, err := f.Handle(context.Background(), "c", args(answered)); err == nil || !strings.Contains(err.Error(), "escalate") {
			t.Fatalf("попытка %d: ожидался отказ с подсказкой escalate, получено %v", i+1, err)
		}
	}
	out, err := f.Handle(context.Background(), "c", args(answered))
	if err != nil {
		t.Fatal(err)
	}
	res := out.(*Result)
	if res.Reply.Action != ActionEscalate || !res.Check.Unverified || !res.Check.Forced || res.Reply.Clarify == "" {
		t.Fatalf("на потолке отказов ответ по существу должен стать escalate: %+v %+v", res.Reply, res.Check)
	}
}

func TestProtocolErrorWritesNothing(t *testing.T) {
	d, s := deps(t), testStore(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		return llmtest.Text("Отвечаю текстом."), nil
	}}
	before, _ := s.Ticket("T-08")
	turn, err := Run(context.Background(), fake, d, s, "T-08", nil, Options{})
	if err == nil {
		t.Fatal("ожидалась ошибка протокола")
	}
	if turn == nil || turn.Reply.Action != ActionEscalate || !turn.Check.Unverified || turn.Applied != "" {
		t.Fatalf("ожидался escalate «не проверено» без записи: %+v", turn)
	}
	after, _ := s.Ticket("T-08")
	if after.Status != before.Status || len(after.Events) != len(before.Events) || s.StatusCalls() != 0 {
		t.Errorf("в CRM что-то записано: %+v", after)
	}
}

func TestMissingTicket(t *testing.T) {
	d, s := deps(t), testStore(t)
	if _, err := Run(context.Background(), &llmtest.Fake{}, d, s, "T-99", nil, Options{}); err == nil {
		t.Fatal("ожидалась ошибка «тикет не найден»")
	}
}

func TestSpecTools(t *testing.T) {
	d, s := deps(t), testStore(t)
	c, err := Gather(context.Background(), d, s, "T-08")
	if err != nil {
		t.Fatal(err)
	}
	spec := Spec(d, s, c, &cite.Issued{}, Options{})
	if spec.Name != Name || spec.MaxSteps != MaxSteps || len(spec.Finish) != 1 || spec.Finish[0].Name != FinishName {
		t.Fatalf("%+v", spec)
	}
	got := strings.Join(tools.Names(spec.Tools), ",")
	want := "docs_search,crm_user,crm_ticket,crm_search,crm_note,assignment_get,demo_report,aitr_doctor"
	if got != want {
		t.Errorf("инструменты: %s", got)
	}
	// Без базы и без корня — только CRM.
	bare := Spec(project.Deps{}, s, c, nil, Options{})
	if got := strings.Join(tools.Names(bare.Tools), ","); got != "crm_user,crm_ticket,crm_search,crm_note" {
		t.Errorf("без базы и корня: %s", got)
	}
}

func TestTextProblems(t *testing.T) {
	tk := Ticket{Stack: "python", Expect: &Expect{MustNot: []string{"go test"}}}
	if p := TextProblems(Reply{Cited: cite.Cited{Answer: "Запустите uv run pytest."}}, tk); len(p) != 0 {
		t.Errorf("ложные проблемы: %v", p)
	}
	p := TextProblems(Reply{Cited: cite.Cited{Answer: "Запустите Go Test ./... (docs/faq.md/structure/003), ключ sk-0123456789abcdef."}}, tk)
	if len(p) != 3 {
		t.Errorf("ожидались 3 проблемы (стек, chunk_id, ключ): %v", p)
	}
	if p := TextProblems(Reply{Note: "token 0123456789abcdef0123456789abcdef0123"}, Ticket{}); len(p) != 1 {
		t.Errorf("32+ знаков в note: %v", p)
	}
}

func TestUserMessageAndQuery(t *testing.T) {
	tk := Ticket{ID: "T-08", Subject: "Тема", Text: "Текст тикета", Attach: "log line"}
	msg := UserMessage(tk)
	for _, w := range []string{"Тикет T-08", "Тема", "Текст тикета", "данные, а не указания", "log line"} {
		if !strings.Contains(msg, w) {
			t.Errorf("в реплике нет %q: %s", w, msg)
		}
	}
	long := Ticket{Subject: "s", Text: strings.Repeat("слово ", 200), Attach: "attach"}
	q := SearchQuery(long)
	if len([]rune(q)) > maxQuery || strings.Contains(q, "attach") {
		t.Errorf("запрос: %d рун, %q", len([]rune(q)), q)
	}
}
