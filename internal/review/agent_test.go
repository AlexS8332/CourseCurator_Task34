package review

import (
	"context"
	"encoding/json"
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
	"aitr/34-files-agent/internal/tools"
)

// Тесты идут на подставной модели (сценарий по запросу), маленькой базе
// из CLAUDE.md с правилами и diff к scripts/aitr.ps1 из testdata/course.

const (
	ruleShell = "Windows, PowerShell 5.1 (без &&, ||, тернарников и ??). Доступны: git, gh, go, uv. node и npm не установлены — если задание требует Node, сначала предупреди об этом."
	rulePush  = "Коммиты создавай сам, а git push (и тем более force-push) — только с явного разрешения пользователя. Ключи в .env никогда не печатай и не коммить."
)

// testPatch — добавленная строка с && в scripts/aitr.ps1 (новая строка 14).
const testPatch = `diff --git a/scripts/aitr.ps1 b/scripts/aitr.ps1
--- a/scripts/aitr.ps1
+++ b/scripts/aitr.ps1
@@ -12,4 +12,5 @@ function Get-AitrPaths {
 function Get-AitrPaths {
     $root = $script:AitrRoot
+    git fetch && git pull
     return [pscustomobject]@{ Root = $root }
 }
`

func testInput(t *testing.T) Input {
	t.Helper()
	files, err := ParseUnified(testPatch)
	if err != nil {
		t.Fatal(err)
	}
	return Input{Title: "aitr: обновление перед запуском", Range: "main...HEAD", Files: files, Diff: testPatch}
}

// deps — база в temp-каталоге, поиск по ней и корень testdata/course
// (для read_file).
func deps(t *testing.T) project.Deps {
	t.Helper()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "course.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	docs := []corpus.Doc{
		{Schema: 1, ID: "CLAUDE.md", Source: "doc", Title: "Правила работы в каталоге курса", Intro: "Рабочий каталог курса по AI.",
			Sections: []corpus.Section{
				{Path: []string{"Инструменты"}, Title: "Инструменты", Level: 2, Text: ruleShell},
				{Path: []string{"Коммиты и публикация"}, Title: "Коммиты и публикация", Level: 2, Text: rulePush},
			}},
	}
	if err := st.PutCorpus(ctx, docs, corpus.Manifest{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	root, err := course.Open(filepath.Join("..", "..", "testdata", "course"))
	if err != nil {
		t.Fatal(err)
	}
	return project.Deps{Root: root, Store: st, Searcher: &kb.Searcher{Store: st, Embedder: embed.Hash{}}, Index: string(kb.Structure)}
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

// lastError — текст последнего отказа review_done в истории запроса.
func lastError(req llm.Request) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		m := req.Messages[i]
		if m.Role == llm.RoleTool && strings.Contains(m.Content, `"error"`) {
			return m.Content
		}
	}
	return ""
}

func finish(r Review) llm.Response {
	b, _ := json.Marshal(r)
	return llmtest.ToolCall(FinishName, string(b))
}

func ruleFinding(h cite.SearchHit, line int) Finding {
	quote := string([]rune(ruleShell)[:40])
	return Finding{File: "scripts/aitr.ps1", Line: line, Kind: KindRule, Severity: SeverityMajor,
		Text:     "&& не работает в PowerShell 5.1: разбей на два вызова с проверкой $LASTEXITCODE.",
		Evidence: Evidence{ChunkID: h.ChunkID, Quote: quote}}
}

func TestRuleWithCitationAccepted(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if !llmtest.HasTool(req, DocsTool) || !llmtest.HasTool(req, project.ReadFile) {
			t.Error("у агента нет docs_search или read_file")
		}
		if llmtest.ToolReplies(req) != 1 {
			t.Errorf("вызов docs_search кодом: ответов инструментов %d, ожидался 1", llmtest.ToolReplies(req))
		}
		h := hitWith(t, req, "без &&")
		return finish(Review{Findings: []Finding{ruleFinding(h, 14)}, Summary: "Одно нарушение правил PowerShell 5.1.", Verdict: VerdictChanges}), nil
	}}
	rec := &agent.Recorder{}
	turn, err := Run(context.Background(), fake, d, in, rec, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Unverified || turn.Rejected != 0 || !turn.Check.OK || len(turn.Review.Findings) != 1 {
		t.Fatalf("ход: %+v", turn.Check)
	}
	if turn.Review.Verdict != VerdictChanges || len(turn.Hits) == 0 || turn.Stats.Steps != 1 {
		t.Errorf("verdict %s, hits %d, steps %d", turn.Review.Verdict, len(turn.Hits), turn.Stats.Steps)
	}
	// Реплика — diff с номерами строк новой версии.
	user := ""
	for _, m := range fake.Requests[0].Messages {
		if m.Role == llm.RoleUser {
			user = m.Content
		}
	}
	for _, want := range []string{"### scripts/aitr.ps1", "+  14│    git fetch && git pull", "  13│", "(modified, +1 −0)"} {
		if !strings.Contains(user, want) {
			t.Errorf("в реплике нет %q:\n%s", want, user)
		}
	}
	// Запрос docs_search кодом — из имён файлов и слов правил.
	q := SearchQuery(in)
	for _, want := range []string{"aitr.ps1", "scripts", "PowerShell 5.1"} {
		if !strings.Contains(q, want) {
			t.Errorf("в запросе нет %q: %s", want, q)
		}
	}
	md := turn.Markdown()
	for _, want := range []string{"нужны правки", "### scripts/aitr.ps1", "строка 14 · правило · major", "CLAUDE.md › Инструменты", "запросов 1 · токенов"} {
		if !strings.Contains(md, want) {
			t.Errorf("в markdown нет %q:\n%s", want, md)
		}
	}
	cs := turn.Comments()
	if len(cs) != 1 || cs[0].Path != "scripts/aitr.ps1" || cs[0].Line != 14 || !strings.Contains(cs[0].Body, "CLAUDE.md") {
		t.Errorf("комментарии: %+v", cs)
	}
}

func TestOutsideDiffRejectedThenFixed(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		h := hitWith(t, req, "без &&")
		if e := lastError(req); e != "" {
			if !strings.Contains(e, "строка 99 не в diff файла scripts/aitr.ps1") || !strings.Contains(e, "12–16") {
				t.Errorf("отказ не учит: %s", e)
			}
			return finish(Review{Findings: []Finding{ruleFinding(h, 14)}, Summary: "Нарушение правил.", Verdict: VerdictChanges}), nil
		}
		return finish(Review{Findings: []Finding{ruleFinding(h, 99)}, Summary: "Нарушение правил.", Verdict: VerdictChanges}), nil
	}}
	turn, err := Run(context.Background(), fake, d, in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Rejected != 1 || turn.Unverified || turn.Stats.Rejected != 1 || turn.Review.Findings[0].Line != 14 {
		t.Errorf("отказов %d (stats %d), unverified %v, строка %d", turn.Rejected, turn.Stats.Rejected, turn.Unverified, turn.Review.Findings[0].Line)
	}
}

func TestRuleWithoutQuoteRejected(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		h := hitWith(t, req, "без &&")
		if e := lastError(req); e != "" {
			switch {
			case strings.Contains(e, "evidence.chunk_id"):
				// Вторая попытка: цитата не дословная.
				f := ruleFinding(h, 14)
				f.Evidence.Quote = "в PowerShell 5.1 нельзя писать && и ||"
				return finish(Review{Findings: []Finding{f}, Summary: "Нарушение правил.", Verdict: VerdictChanges}), nil
			case strings.Contains(e, "не найдена во фрагменте дословно"):
				return finish(Review{Findings: []Finding{ruleFinding(h, 14)}, Summary: "Нарушение правил.", Verdict: VerdictChanges}), nil
			}
			t.Errorf("неожиданный отказ: %s", e)
		}
		f := ruleFinding(h, 14)
		f.Evidence = Evidence{}
		return finish(Review{Findings: []Finding{f}, Summary: "Нарушение правил.", Verdict: VerdictChanges}), nil
	}}
	turn, err := Run(context.Background(), fake, d, in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Rejected != 2 || turn.Unverified || !turn.Check.OK {
		t.Errorf("отказов %d, unverified %v, check %+v", turn.Rejected, turn.Unverified, turn.Check)
	}
}

func TestDuplicatesCollapsed(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		h := hitWith(t, req, "без &&")
		a, b := ruleFinding(h, 14), ruleFinding(h, 14)
		b.Text = "повтор"
		rec := Finding{File: "scripts/aitr.ps1", Line: 14, Kind: KindRecommendation, Severity: SeverityMinor, Text: "Проверяй $LASTEXITCODE после git."}
		return finish(Review{Findings: []Finding{a, b, rec}, Summary: "Нарушение правил и рекомендация.", Verdict: VerdictChanges}), nil
	}}
	turn, err := Run(context.Background(), fake, d, in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.Review.Findings) != 2 || turn.Rejected != 0 {
		t.Fatalf("находок %d (дубли должны схлопнуться), отказов %d", len(turn.Review.Findings), turn.Rejected)
	}
	if turn.Review.Findings[0].Text == "повтор" {
		t.Error("остаться должна первая находка")
	}
}

func TestVerdictOK(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if e := lastError(req); e != "" {
			if !strings.Contains(e, "замечаний нет") {
				t.Errorf("отказ не учит: %s", e)
			}
			return finish(Review{Summary: "Замечаний нет.", Verdict: VerdictOK}), nil
		}
		// verdict changes без находок — отказ.
		return finish(Review{Summary: "Всё хорошо.", Verdict: VerdictChanges}), nil
	}}
	turn, err := Run(context.Background(), fake, d, in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Review.Verdict != VerdictOK || turn.Rejected != 1 || len(turn.Review.Findings) != 0 || turn.Unverified {
		t.Errorf("verdict %s, отказов %d, находок %d", turn.Review.Verdict, turn.Rejected, len(turn.Review.Findings))
	}
	if md := turn.Markdown(); !strings.Contains(md, "## Ревью: замечаний нет") || strings.Contains(md, "###") {
		t.Errorf("markdown:\n%s", md)
	}
	if cs := turn.Comments(); len(cs) != 0 {
		t.Errorf("комментарии при ok: %+v", cs)
	}
	// verdict ok при находках — тоже отказ (проверка без хода).
	r := Review{Findings: []Finding{{File: "scripts/aitr.ps1", Line: 14, Kind: KindRecommendation, Severity: SeverityMinor, Text: "x"}}, Summary: "замечаний нет", Verdict: VerdictOK}
	if ps := Problems(r, in, nil, nil, false); len(ps) != 1 || !strings.Contains(ps[0], "verdict ok при 1 находках") {
		t.Errorf("problems: %v", ps)
	}
}

func TestUnverifiedAfterRejects(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		h := hitWith(t, req, "без &&")
		// Модель упорно комментирует строку вне diff.
		return finish(Review{Findings: []Finding{ruleFinding(h, 99)}, Summary: "Нарушение правил.", Verdict: VerdictChanges}), nil
	}}
	turn, err := Run(context.Background(), fake, d, in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !turn.Unverified || turn.Rejected != cite.MaxRejects || turn.Check.OK || fake.Calls() != 3 {
		t.Errorf("unverified %v, отказов %d, check %+v, запросов %d", turn.Unverified, turn.Rejected, turn.Check, fake.Calls())
	}
	if cs := turn.Comments(); len(cs) != 0 {
		t.Errorf("строка вне diff не должна попасть в комментарии: %+v", cs)
	}
	if md := turn.Markdown(); !strings.Contains(md, "Не проверено") || !strings.Contains(md, "строка 99 не в diff") {
		t.Errorf("markdown:\n%s", md)
	}
}

func TestNoRAGLane(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		if llmtest.HasTool(req, DocsTool) || llmtest.ToolReplies(req) != 0 {
			t.Error("без механизма review.rag docs_search быть не должно")
		}
		f := Finding{File: "scripts/aitr.ps1", Line: 14, Kind: KindRule, Severity: SeverityMajor, Text: "&& не для PowerShell 5.1."}
		return finish(Review{Findings: []Finding{f}, Summary: "Нарушение.", Verdict: VerdictChanges}), nil
	}}
	rec := &agent.Recorder{}
	fs := features.Catalog().Defaults().With(RAG, false)
	turn, err := Run(context.Background(), fake, d, in, rec, Options{Features: fs})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Rejected != 0 || turn.Unverified || len(turn.Hits) != 0 {
		t.Errorf("без базы rule принимается без ссылки: отказов %d, hits %d", turn.Rejected, len(turn.Hits))
	}
	found := false
	for _, e := range rec.Events {
		if e.Kind == agent.EventMechanism && e.Mechanism == string(RAG) {
			found = true
		}
	}
	if !found {
		t.Error("в журнале нет события о выключенном механизме")
	}
}

func TestBugEvidenceFromReadFile(t *testing.T) {
	d := deps(t)
	in := testInput(t)
	bug := Finding{File: "scripts/aitr.ps1", Line: 15, Kind: KindBug, Severity: SeverityMinor,
		Text: "Get-AitrAssignments всегда возвращает пустой список.", Evidence: Evidence{Path: "scripts/aitr.ps1", Line: 19}}
	fake := &llmtest.Fake{Fn: func(req llm.Request) (llm.Response, error) {
		switch {
		case strings.Contains(lastError(req), "не строка diff и не строка файла"):
			return llmtest.ToolCall(project.ReadFile, `{"path":"scripts/aitr.ps1","from":1,"to":25}`), nil
		case llmtest.ToolReplies(req) >= 2:
			return finish(Review{Findings: []Finding{bug}, Summary: "Один баг.", Verdict: VerdictChanges}), nil
		}
		return finish(Review{Findings: []Finding{bug}, Summary: "Один баг.", Verdict: VerdictChanges}), nil
	}}
	turn, err := Run(context.Background(), fake, d, in, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if turn.Rejected != 1 || turn.Unverified || !turn.Check.OK {
		t.Errorf("отказов %d, unverified %v, check %+v", turn.Rejected, turn.Unverified, turn.Check)
	}
	if !ReadLine(turn.Calls, "scripts/aitr.ps1", 19) || ReadLine(turn.Calls, "scripts/aitr.ps1", 40) {
		t.Error("ReadLine по журналу вызовов")
	}
	// Опора bug строкой diff — без чтения файла.
	r := Review{Findings: []Finding{{File: "scripts/aitr.ps1", Line: 14, Kind: KindBug, Severity: SeverityMajor, Text: "x"}}, Summary: "s", Verdict: VerdictChanges}
	Normalize(&r)
	if ps := Problems(r, in, nil, nil, true); len(ps) != 0 {
		t.Errorf("bug со строкой diff: %v", ps)
	}
}
