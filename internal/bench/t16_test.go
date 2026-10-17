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
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/support"
	"aitr/34-files-agent/internal/tools"
)

// --- И-16 на подставной модели -------------------------------------------

const (
	faqKeys  = "Если ключ DEEPSEEK_API_KEY не задан, aitr doctor печатает FAIL: добавь строку с ключом в .env корня курса и повтори aitr doctor."
	faqRace  = "Для go test -race нужен gcc из mingw-w64 в PATH: системный gcc из msys2 — cygwin-сборка, и -race с ним не собирается."
	faqTests = "Тесты задания запускает aitr test NN: для стека go это go vet и go test, для стека python — uv run pytest в каталоге задания."
)

func t16Deps(t *testing.T) project.Deps {
	t.Helper()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "course.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	docs := []corpus.Doc{
		{Schema: 1, ID: "docs/faq.md", Source: "doc", Title: "FAQ оснастки", Intro: "Вопросы об aitr и edgerec.",
			Sections: []corpus.Section{
				{Path: []string{"Ключи"}, Title: "Ключи", Level: 2, Text: faqKeys},
				{Path: []string{"Тесты"}, Title: "Тесты", Level: 2, Text: faqTests},
			}},
		{Schema: 1, ID: "docs/cards/28-local-llm.md", Source: "card", Title: "28 — Локальная LLM", Intro: "Карточка задания 28.",
			Sections: []corpus.Section{{Path: []string{"Грабли"}, Title: "Грабли", Level: 2, Text: faqRace}}},
	}
	if err := st.PutCorpus(ctx, docs, corpus.Manifest{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	return project.Deps{Store: st, Searcher: &kb.Searcher{Store: st, Embedder: embed.Hash{}}, Index: string(kb.Structure)}
}

const t16Users = `[
{"id":"u-go","name":"Автор","role":"author","os":"Windows 10","shell":"PowerShell 5.1","tools":{"go":true,"uv":true},"assignments":["28"]},
{"id":"u-py","name":"Дмитрий","role":"peer","os":"macOS","shell":"zsh","tools":{"go":true,"uv":true},"assignments":[]}]`

const t16Tickets = `[
{"id":"T-01","user":"u-go","stack":"go","subject":"Авторизация","text":"Почему не работает авторизация? Пишет, что нужен DEEPSEEK_API_KEY.","status":"open",
 "expect":{"must":[["не задан"]],"sources":["docs/faq.md"],"action":"resolve"}},
{"id":"T-02","user":"u-go","num":"28","stack":"go","subject":"go test -race падает","text":"go test -race падает на cgo: что делать?","status":"open",
 "expect":{"must":[["mingw"]],"sources":["docs/cards/28-local-llm.md"],"action":"resolve"}},
{"id":"T-03","user":"u-go","stack":"go","subject":"Тесты","text":"тесты не запускаются, что делать?","status":"open",
 "expect":{"must":[["go test","go vet"]],"must_not":["pytest","uv run"],"action":"resolve"}},
{"id":"T-04","user":"u-py","stack":"python","subject":"Тесты","text":"тесты не запускаются, что делать?","status":"open",
 "expect":{"must":[["pytest","uv run"]],"must_not":["go test","go vet"],"action":"resolve"}},
{"id":"T-05","user":"u-py","subject":"Kubernetes","text":"Как развернуть задание в Kubernetes?","status":"open","expect":{"action":"escalate"}}]`

func writeSet(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, support.UsersFile), []byte(t16Users), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, support.TicketsFile), []byte(t16Tickets), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// contextStack — стек из блока «Контекст тикета» в запросе; пусто — блока нет.
func contextStack(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role != llm.RoleSystem || !strings.HasPrefix(m.Content, support.BlockTitle) {
			continue
		}
		for _, s := range []string{"go", "python"} {
			if strings.Contains(m.Content, "стек "+s) {
				return s
			}
		}
	}
	return ""
}

// hitWith16 — фрагмент выдачи docs_search с подстрокой.
func hitWith16(req llm.Request, sub string) (cite.SearchHit, bool) {
	for _, m := range req.Messages {
		if m.Role != llm.RoleTool || !strings.Contains(m.Content, `"hits"`) {
			continue
		}
		content, _ := tools.Unwrap(m.Content)
		var out cite.SearchResult
		if json.Unmarshal([]byte(content), &out) != nil {
			continue
		}
		for _, h := range out.Hits {
			if strings.Contains(h.Text, sub) {
				return h, true
			}
		}
	}
	return cite.SearchHit{}, false
}

func reply16(r support.Reply) llm.Response {
	b, _ := json.Marshal(r)
	return llmtest.ToolCall(support.FinishName, string(b))
}

func cited16(h cite.SearchHit, answer string) support.Reply {
	quote := string([]rune(h.Text)[:min(60, len([]rune(h.Text)))])
	return support.Reply{Cited: cite.Cited{Answer: answer, Sources: []cite.CitedSource{{ChunkID: h.ChunkID}},
		Quotes: []cite.CitedQuote{{ChunkID: h.ChunkID, Text: quote}}}, Action: support.ActionResolve, Note: "по FAQ"}
}

// scenario16 — подставная модель: по корпусу цитирует первый подходящий
// фрагмент; близнецам отвечает под стек из блока контекста, а без блока —
// одинаково, под go (crm_user не зовёт); вне корпуса — escalate.
func scenario16(req llm.Request) (llm.Response, error) {
	q := firstUser(req)
	switch {
	case strings.Contains(q, "Kubernetes"):
		return reply16(support.Reply{Cited: cite.Cited{Answer: "В документации оснастки о Kubernetes ничего нет.",
			Clarify: "Речь о задании курса или о внешнем проекте?"}, Action: support.ActionEscalate}), nil
	case strings.Contains(q, "тесты не запускаются"):
		h, _ := hitWith16(req, "uv run pytest")
		if contextStack(req) == "python" {
			return reply16(cited16(h, "Запустите uv run pytest в каталоге задания — так тесты python запускает aitr test (FAQ, «Тесты»).")), nil
		}
		return reply16(cited16(h, "Запустите go vet и go test — так тесты запускает aitr test (FAQ, «Тесты»).")), nil
	case strings.Contains(q, "race"):
		h, _ := hitWith16(req, "mingw")
		return reply16(cited16(h, "Нужен gcc из mingw-w64 в PATH: системный gcc из msys2 с -race не собирается (карточка 28, «Грабли»).")), nil
	}
	h, _ := hitWith16(req, "не задан")
	return reply16(cited16(h, "Ключ DEEPSEEK_API_KEY не задан: добавьте строку с ключом в .env корня и повторите aitr doctor (FAQ, «Ключи»).")), nil
}

func TestT16Fake(t *testing.T) {
	d := t16Deps(t)
	set := writeSet(t)
	work := t.TempDir()
	fake := &llmtest.Fake{Fn: scenario16}
	var log strings.Builder
	results, err := T16(context.Background(), T16Options{Deps: d, LLM: fake, Model: "fake", Set: set, Work: work, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if len(results) != 1 {
		t.Fatalf("стендов %d", len(results))
	}
	r := results[0]
	for _, c := range r.Checks {
		if !c.OK {
			t.Errorf("проверка «%s» (%s) не прошла: %s, нужно %s", c.What, c.Lane, c.Got, c.Need)
		}
	}
	if r.Mechanism != support.Mechanism || r.Requests == 0 || len(r.Rows) != 15 {
		t.Errorf("стенд: механизм %s, запросов %d, строк %d", r.Mechanism, r.Requests, len(r.Rows))
	}
	// Эталонный набор не тронут, копии дорожек — с изменёнными статусами.
	src, err := support.Open(set)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range src.Tickets() {
		if tk.Status != support.StatusOpen {
			t.Errorf("набор испорчен: %s %s", tk.ID, tk.Status)
		}
	}
	copyMain, err := support.Open(filepath.Join(work, "main"))
	if err != nil {
		t.Fatal(err)
	}
	if tk, _ := copyMain.Ticket("T-01"); tk.Status != support.StatusResolved {
		t.Errorf("в копии main T-01 должен быть resolved: %s", tk.Status)
	}
	if tk, _ := copyMain.Ticket("T-05"); tk.Status != support.StatusEscalated {
		t.Errorf("в копии main T-05 должен быть escalated: %s", tk.Status)
	}
	// Без контекста близнец python получил ответ под go: must_not отказал,
	// после потолка отказов ответ принят «не проверено» и близнецов по
	// своему стеку меньше.
	got := map[string]float64{}
	for _, m := range r.Metrics {
		if m.Name == "близнецы по своему стеку" {
			got[m.Lane] = m.Value
		}
	}
	if got["main"] != 2 || got["noctx"] != 1 || got["same"] != 2 {
		t.Errorf("близнецы: %v", got)
	}
	md := Markdown(results)
	for _, want := range []string{Trial16, "пройдено", "support.context", "близнецы"} {
		if !strings.Contains(md, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
	mdPath, jsPath, err := Write(t.TempDir(), "t16", results)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mdPath, jsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

func TestCategorizeAndExpect(t *testing.T) {
	var tickets []support.Ticket
	if err := json.Unmarshal([]byte(t16Tickets), &tickets); err != nil {
		t.Fatal(err)
	}
	kinds := Categorize(tickets)
	want := map[string]string{"T-01": TicketCorpus, "T-02": TicketCorpus, "T-03": TicketTwin, "T-04": TicketTwin, "T-05": TicketOut}
	for id, k := range want {
		if kinds[id] != k {
			t.Errorf("%s: %s, ожидалось %s", id, kinds[id], k)
		}
	}
	e := &support.Expect{Must: [][]string{{"pytest", "uv run"}}, MustNot: []string{"go test"}, Sources: []string{"docs/faq.md"}}
	if !ExpectOK("Запустите UV RUN pytest.", e) || ExpectOK("go test и uv run", e) || ExpectOK("ничего", e) {
		t.Error("ExpectOK")
	}
	if !SourcesOK([]cite.CitedSource{{ChunkID: "docs/faq.md/structure/002"}}, e) || SourcesOK([]cite.CitedSource{{ChunkID: "docs/faq.md.bak/structure/001"}}, e) {
		t.Error("SourcesOK")
	}
	if !SourcesOK(nil, nil) || !ExpectOK("x", nil) {
		t.Error("без ожиданий — всегда да")
	}
}

func TestT16NeedsSet(t *testing.T) {
	if _, err := T16(context.Background(), T16Options{LLM: &llmtest.Fake{}, Set: t.TempDir()}); err == nil {
		t.Fatal("без users.json/tickets.json — ошибка")
	}
}
