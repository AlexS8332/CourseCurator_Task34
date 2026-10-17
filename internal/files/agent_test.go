package files

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
)

// Тесты этапов на подставной модели во временном git-репозитории —
// копии testdata/course. Сеть не нужна.

const (
	cardA = "docs/cards/01-hello-go.md"
	cardB = "docs/cards/02-rag-basics.md"
)

// initCourse — копия testdata/course во временном каталоге с git init и
// коммитом «база»; возвращает Deps над ней.
func initCourse(t *testing.T) (project.Deps, string) {
	t.Helper()
	if err := gitx.Available(); err != nil {
		t.Skip(err)
	}
	src := filepath.Join("..", "..", "testdata", "course")
	dir := filepath.Join(t.TempDir(), "course")
	err := filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(dir, rel), 0o755)
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dir, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("sandbox/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	g := gitx.Repo{Dir: dir}
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.name", "test"}, {"config", "user.email", "test@localhost"},
		{"config", "core.autocrlf", "false"}, {"config", "commit.gpgsign", "false"}, {"add", "-A"}, {"commit", "-q", "-m", "база"}} {
		if _, err := g.Run(ctx, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	root, err := course.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return project.Deps{Root: root}, dir
}

// stageOf — этап по системному промпту запроса.
func stageOf(req llm.Request) Stage {
	if len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "этап планирования") {
		return StagePlan
	}
	return StageApply
}

// turn — номер запроса внутри этапа: сколько ответов модели уже в истории.
func turn(req llm.Request) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			n++
		}
	}
	return n
}

func planCall(p Plan) llm.Response {
	b, _ := json.Marshal(p)
	return llmtest.ToolCall(PlanFinish, string(b))
}

func doneCall(d Done) llm.Response {
	b, _ := json.Marshal(d)
	return llmtest.ToolCall(DoneFinish, string(b))
}

func writeCall(path, content string) llm.Response {
	b, _ := json.Marshal(map[string]string{"path": path, "content": content})
	return llmtest.ToolCall(WriteFile, string(b))
}

var twoFiles = Plan{Goal: "обновить две карточки", Writes: true,
	Files: []PlanFile{{Path: cardA, Why: "добавить вывод"}, {Path: cardB, Why: "добавить вывод"}},
	Steps: []string{"прочитать карточки", "дописать выводы", "закоммитить"}}

// scripted — сценарий по этапам и номерам запросов; чего нет — текст
// (тогда Runner напоминает о финишёре, и ход упадёт — это видно в тесте).
type scripted struct {
	plan, apply []llm.Response
	seen        []llm.Request
}

func (s *scripted) fn(req llm.Request) (llm.Response, error) {
	s.seen = append(s.seen, req)
	list := s.plan
	if stageOf(req) == StageApply {
		list = s.apply
	}
	i := turn(req)
	if i < len(list) {
		return list[i], nil
	}
	return llmtest.Text("сценарий кончился"), nil
}

func TestPlanHasNoWriteTools(t *testing.T) {
	d, dir := initCourse(t)
	ctx := context.Background()
	sc := &scripted{plan: []llm.Response{
		llmtest.ToolCall(project.ReadFile, `{"path":"`+cardA+`"}`),
		planCall(twoFiles),
	}}
	fake := &llmtest.Fake{Fn: sc.fn}
	rec := &agent.Recorder{}
	r, err := New(ctx, d, "допиши выводы в карточки 01 и 02", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DoPlan(ctx, fake, d, rec, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	if r.Stage != StageApprove || r.Plan == nil || len(r.Plan.Files) != 2 || !r.Plan.Writes {
		t.Fatalf("после плана: этап %s, план %+v", r.Stage, r.Plan)
	}
	for i, req := range sc.seen {
		for _, w := range WriteTools {
			if llmtest.HasTool(req, w) {
				t.Errorf("запрос %d: на этапе plan выдан %s", i+1, w)
			}
		}
		for _, want := range []string{project.ReadFile, project.RepoTree, project.RepoSearch, project.GitStatus, PlanFinish} {
			if !llmtest.HasTool(req, want) {
				t.Errorf("запрос %d: нет инструмента %s", i+1, want)
			}
		}
		if llmtest.HasTool(req, project.DocsSearch) {
			t.Errorf("запрос %d: docs_search без базы", i+1)
		}
	}
	if r.WritesBeforeApprove != 0 || r.WritesOutsidePlan != 0 || r.Worktree != "" {
		t.Errorf("счётчики: до согласия %d, вне плана %d, worktree %q", r.WritesBeforeApprove, r.WritesOutsidePlan, r.Worktree)
	}
	if plan, apply := r.Steps(); plan != 2 || apply != 0 {
		t.Errorf("шаги: план %d, выполнение %d", plan, apply)
	}
	if _, err := os.Stat(filepath.Join(dir, "sandbox")); err == nil {
		t.Error("на этапе plan появился sandbox")
	}
	if !strings.Contains(r.Report(), "появятся после согласия") {
		t.Error("в отчёте нет подсказки о правах")
	}
	// Повторный DoPlan — не тот этап.
	if err := r.DoPlan(ctx, fake, d, nil, Options{}); err == nil {
		t.Error("второй DoPlan должен отказать")
	}
}

func TestNoGatesCountsWritesBeforeApprove(t *testing.T) {
	d, dir := initCourse(t)
	ctx := context.Background()
	sc := &scripted{plan: []llm.Response{
		writeCall(cardA, "# перезаписано до согласия\n"),
		planCall(twoFiles),
	}}
	fake := &llmtest.Fake{Fn: sc.fn}
	fs := features.Catalog().Defaults().With(Gates, false)
	r, err := New(ctx, d, "допиши выводы", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DoPlan(ctx, fake, d, nil, Options{Model: "fake", Features: fs}); err != nil {
		t.Fatal(err)
	}
	if !llmtest.HasTool(sc.seen[0], WriteFile) || !llmtest.HasTool(sc.seen[0], GitCommit) {
		t.Error("без прав этапа инструменты записи должны быть выданы на plan")
	}
	if !strings.Contains(sc.seen[0].Messages[0].Content, "до согласия") {
		t.Error("в промпте контрольной дорожки нет абзаца о запрете до согласия")
	}
	if r.WritesBeforeApprove != 1 || r.Writes() != 1 {
		t.Errorf("записей до согласия %d (всего %d), ожидалась 1", r.WritesBeforeApprove, r.Writes())
	}
	data, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(cardA)))
	if strings.Contains(string(data), "перезаписано") {
		t.Error("запись до согласия попала в рабочую копию")
	}
	if !strings.Contains(llmtest.LastToolReply(sc.seen[1]), "до согласия") {
		t.Errorf("отказ инструмента должен объяснять этап: %s", llmtest.LastToolReply(sc.seen[1]))
	}
	if r.Stage != StageApprove {
		t.Errorf("этап %s", r.Stage)
	}
}

func TestPlanRejectsBadPaths(t *testing.T) {
	d, _ := initCourse(t)
	ctx := context.Background()
	bad := Plan{Goal: "x", Writes: true, Steps: []string{"a"},
		Files: []PlanFile{{Path: "INDEX.md", Why: "нельзя"}, {Path: cardA, Why: "ok"}}}
	missing := Plan{Goal: "x", Writes: true, Steps: []string{"a"},
		Files: []PlanFile{{Path: "docs/cards/99-none.md", Why: "нет и не new"}, {Path: cardA, Why: "ok"}}}
	good := Plan{Goal: "x", Writes: true, Steps: []string{"a"},
		Files: []PlanFile{{Path: "docs/checks/new.md", New: true, Why: "отчёт"}, {Path: cardA, Why: "ok"}}}
	sc := &scripted{plan: []llm.Response{planCall(bad), planCall(missing), planCall(good)}}
	fake := &llmtest.Fake{Fn: sc.fn}
	r, err := New(ctx, d, "цель", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DoPlan(ctx, fake, d, nil, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	if r.Rejected != 2 || r.Unverified {
		t.Errorf("отказов %d, не проверено %v", r.Rejected, r.Unverified)
	}
	for i, want := range []string{"INDEX.md", "99-none.md"} {
		if reply := llmtest.LastToolReply(sc.seen[i+1]); !strings.Contains(reply, want) {
			t.Errorf("отказ %d должен назвать %s: %s", i+1, want, reply)
		}
	}
	if r.Plan == nil || r.Plan.Files[0].Path != "docs/checks/new.md" {
		t.Errorf("принят не тот план: %+v", r.Plan)
	}
}

func TestApproveByWords(t *testing.T) {
	d, dir := initCourse(t)
	ctx := context.Background()
	sc := &scripted{plan: []llm.Response{planCall(twoFiles)}}
	r, err := New(ctx, d, "цель", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Approve(ctx, "да", Options{}); err == nil {
		t.Error("Approve до плана должен отказать")
	}
	if err := r.DoPlan(ctx, &llmtest.Fake{Fn: sc.fn}, d, nil, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	for _, reply := range []string{"нет", "не применяй", "не утверждаю, перепланируй"} {
		ok, err := r.Approve(ctx, reply, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if ok || r.Stage != StageApprove || r.Worktree != "" {
			t.Errorf("после «%s»: ok %v, этап %s, worktree %q", reply, ok, r.Stage, r.Worktree)
		}
	}
	// Новый прогон: «нет», DoApply без согласия, затем «да, применяй».
	r2, _ := New(ctx, d, "цель", Options{})
	if err := r2.DoPlan(ctx, &llmtest.Fake{Fn: (&scripted{plan: []llm.Response{planCall(twoFiles)}}).fn}, d, nil, Options{Model: "fake"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r2.Approve(ctx, "нет", Options{}); ok || r2.Stage != StageApprove {
		t.Fatalf("«нет»: ok %v, этап %s", ok, r2.Stage)
	}
	if err := r2.DoApply(ctx, &llmtest.Fake{}, d, nil, Options{}); err == nil {
		t.Error("DoApply без согласия должен отказать")
	}
	ok, err := r2.Approve(ctx, "да, применяй", Options{})
	if err != nil || !ok || r2.Stage != StageApply {
		t.Fatalf("«да, применяй»: ok %v, %v, этап %s", ok, err, r2.Stage)
	}
	if r2.Worktree == "" || !strings.HasPrefix(r2.Branch, "agent/") {
		t.Fatalf("worktree %q, ветка %q", r2.Worktree, r2.Branch)
	}
	if rel, _ := filepath.Rel(dir, r2.Worktree); filepath.ToSlash(rel) != "sandbox/agent/"+r2.ID {
		t.Errorf("worktree не в sandbox/agent/<id>: %s", rel)
	}
	if b, _ := (gitx.Repo{Dir: r2.Worktree}).Branch(ctx); b != r2.Branch {
		t.Errorf("ветка worktree %s, в прогоне %s", b, r2.Branch)
	}
	if r2.BaseHead() == "" {
		t.Error("база worktree не записана")
	}
}

func TestApplyWritesOnlyPlanFiles(t *testing.T) {
	d, dir := initCourse(t)
	ctx := context.Background()
	sc := &scripted{
		plan: []llm.Response{planCall(twoFiles)},
		apply: []llm.Response{
			writeCall("docs/notes/02-rag.md", "вне плана\n"),
			writeCall(cardA, "# 01 — hello go\n\n## Выводы\n\nготово\n"),
			doneCall(Done{Changed: []string{cardA}, Summary: "дописал одну карточку"}),
			writeCall(cardB, "# 02 — rag basics\n\n## Выводы\n\nготово\n"),
			llmtest.ToolCall(GitCommit, `{"message":"карточки: выводы"}`),
			doneCall(Done{Changed: []string{cardA, cardB}, Summary: "дописал выводы в обе карточки"}),
		},
	}
	fake := &llmtest.Fake{Fn: sc.fn}
	rec := &agent.Recorder{}
	r, err := New(ctx, d, "допиши выводы", Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Model: "fake"}
	if err := r.DoPlan(ctx, fake, d, rec, o); err != nil {
		t.Fatal(err)
	}
	if ok, err := r.Approve(ctx, "", Options{Yes: true}); err != nil || !ok {
		t.Fatalf("-yes: %v %v", ok, err)
	}
	if err := r.DoApply(ctx, fake, d, rec, o); err != nil {
		t.Fatalf("%v\n%s", err, r.Report())
	}
	if r.Stage != StageDone || r.Done == nil {
		t.Fatalf("этап %s, done %+v", r.Stage, r.Done)
	}
	// Запрос этапа apply: чтение плюс инструменты записи, finisher files_done.
	first := sc.seen[1]
	for _, want := range append([]string{project.ReadFile, DoneFinish}, WriteTools...) {
		if !llmtest.HasTool(first, want) {
			t.Errorf("на apply нет %s", want)
		}
	}
	// Вне плана — отказ инструмента с объяснением.
	if reply := llmtest.LastToolReply(sc.seen[2]); !strings.Contains(reply, "нет в плане") || !strings.Contains(reply, cardA) {
		t.Errorf("отказ вне плана: %s", reply)
	}
	// files_done с одним файлом при плане с записью — отказ с объяснением.
	if reply := llmtest.LastToolReply(sc.seen[4]); !strings.Contains(reply, "один файл") || !strings.Contains(reply, "в плане 2") {
		t.Errorf("отказ files_done: %s", reply)
	}
	if r.WritesOutsidePlan != 1 || r.WritesBeforeApprove != 0 || r.Writes() != 4 {
		t.Errorf("вне плана %d, до согласия %d, всего %d", r.WritesOutsidePlan, r.WritesBeforeApprove, r.Writes())
	}
	if r.Rejected != 1 || r.Unverified {
		t.Errorf("отказов %d, не проверено %v", r.Rejected, r.Unverified)
	}
	if strings.Join(r.Changed, ",") != cardA+","+cardB {
		t.Errorf("изменённые: %v", r.Changed)
	}
	if r.Commit == "" || r.Commit == r.BaseHead() {
		t.Errorf("коммит %q (база %q)", r.Commit, r.BaseHead())
	}
	if !strings.Contains(r.Diff, "+готово") || !strings.Contains(r.Diff, cardB) {
		t.Errorf("diff:\n%s", r.Diff)
	}
	// Рабочая копия автора не тронута, файл вне плана — тоже.
	for _, p := range []string{cardA, "docs/notes/02-rag.md"} {
		data, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if strings.Contains(string(data), "готово") || strings.Contains(string(data), "вне плана") {
			t.Errorf("%s изменён в корне", p)
		}
	}
	if st, _ := (gitx.Repo{Dir: dir}).Status(ctx); len(st) != 0 {
		t.Errorf("корень грязный: %v", st)
	}
	// Diff применим к базе.
	if err := (gitx.Repo{Dir: dir}).ApplyCheck(ctx, r.Diff); err != nil {
		t.Errorf("git apply --check: %v", err)
	}
	md := r.Report()
	for _, want := range []string{"## План", "## Ветка", r.Branch, "## Изменённые файлы", "## Diff", "## Счётчики", "дописал выводы"} {
		if !strings.Contains(md, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
	if plan, apply := r.Steps(); plan != 1 || apply != 6 {
		t.Errorf("шаги: план %d, выполнение %d", plan, apply)
	}
	if len(r.Calls) == 0 {
		t.Error("журнал вызовов пуст")
	}
	if err := r.DoApply(ctx, fake, d, nil, o); err == nil {
		t.Error("повторный DoApply должен отказать")
	}
}

func TestApplyReadOnlyPlan(t *testing.T) {
	d, _ := initCourse(t)
	ctx := context.Background()
	readOnly := Plan{Goal: "найти упоминания", Writes: false, Steps: []string{"поискать"},
		Files: []PlanFile{{Path: cardA, Why: "читать"}}}
	sc := &scripted{
		plan:  []llm.Response{planCall(readOnly)},
		apply: []llm.Response{doneCall(Done{Summary: "- " + cardA + ":1 — # 01"})},
	}
	fake := &llmtest.Fake{Fn: sc.fn}
	r, err := New(ctx, d, "где упоминается", Options{})
	if err != nil {
		t.Fatal(err)
	}
	o := Options{Model: "fake", Yes: true}
	if err := r.DoPlan(ctx, fake, d, nil, o); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Approve(ctx, "", o); err != nil {
		t.Fatal(err)
	}
	if err := r.DoApply(ctx, fake, d, nil, o); err != nil {
		t.Fatal(err)
	}
	if len(r.Changed) != 0 || r.Commit != "" || strings.TrimSpace(r.Diff) != "" || r.Stage != StageDone {
		t.Errorf("план без записи: изменено %v, коммит %q, diff %q, этап %s", r.Changed, r.Commit, r.Diff, r.Stage)
	}
}

func TestRunScriptChecks(t *testing.T) {
	d, _ := initCourse(t)
	ctx := context.Background()
	sc := &scripted{
		plan: []llm.Response{planCall(twoFiles)},
		apply: []llm.Response{
			writeCall(cardA, "# 01\n\n## Выводы\n\nготово\n"),
			writeCall(cardB, "# 02\n\n## Выводы\n\nготово\n"),
			doneCall(Done{Changed: []string{cardA, cardB}, Summary: "готово"}),
		},
	}
	s := Script{ID: "T1", Goal: "допиши выводы", Expect: Expect{FilesMin: 2, Writes: true, FilesAllowed: []string{"docs/cards/"},
		Checks: []Check{{Kind: CheckExists, Path: cardA}, {Kind: CheckContains, Path: cardB, Text: "## Выводы"}, {Kind: CheckAbsent, Path: cardB, Text: "TODO"}}}}
	r, results, err := RunScript(ctx, &llmtest.Fake{Fn: sc.fn}, d, s, nil, Options{Model: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Stage != StageDone || len(r.Changed) != 2 || r.Commit == "" {
		t.Errorf("прогон: этап %s, изменено %v, коммит %q", r.Stage, r.Changed, r.Commit)
	}
	if len(results) != 6 || !AllOK(results) {
		for _, c := range results {
			t.Logf("%s %s %s: %v %s", c.Check.Kind, c.Check.Path, c.Check.Text, c.OK, c.Got)
		}
		t.Errorf("проверок %d, все ли прошли: %v", len(results), AllOK(results))
	}
	if _, _, err := RunScript(ctx, &llmtest.Fake{}, d, Script{}, nil, Options{}); err == nil {
		t.Error("пустой сценарий должен отказать")
	}
}

func TestSessions(t *testing.T) {
	s := NewSessions()
	if _, ok := s.Get("x"); ok || s.Len() != 0 {
		t.Fatal("пустое хранилище")
	}
	a, b := &Run{ID: "20260101-000002-b", Goal: "b"}, &Run{ID: "20260101-000001-a", Goal: "a"}
	s.Put(a)
	s.Put(b)
	s.Put(nil)
	s.Put(&Run{})
	if got, ok := s.Get(a.ID); !ok || got != a {
		t.Error("Get")
	}
	if l := s.List(); len(l) != 2 || l[0] != b || l[1] != a {
		t.Errorf("List: %v", l)
	}
	s.Remove(a.ID)
	if _, ok := s.Get(a.ID); ok || s.Len() != 1 {
		t.Error("Remove")
	}
	var nilS *Sessions
	nilS.Put(a)
	if _, ok := nilS.Get(a.ID); ok || nilS.List() != nil || nilS.Len() != 0 {
		t.Error("nil-хранилище")
	}
}

func TestDoneProblems(t *testing.T) {
	p := &Plan{Writes: true, Files: []PlanFile{{Path: cardA}, {Path: cardB}}}
	if pr := DoneProblems(Done{Changed: []string{cardA}, Summary: "x"}, []string{cardA}, p); len(pr) != 1 || !strings.Contains(pr[0], "один файл, в плане 2") {
		t.Errorf("один файл: %v", pr)
	}
	if pr := DoneProblems(Done{Changed: []string{cardA}, Summary: "x"}, []string{cardA, cardB}, p); len(pr) != 1 || !strings.Contains(pr[0], "нет в changed") {
		t.Errorf("пропущенный: %v", pr)
	}
	if pr := DoneProblems(Done{Changed: []string{cardA, cardB, "docs/x.md"}, Summary: "x"}, []string{cardA, cardB}, p); len(pr) != 1 || !strings.Contains(pr[0], "не изменены") {
		t.Errorf("лишний: %v", pr)
	}
	if pr := DoneProblems(Done{Changed: []string{cardA, cardB}}, []string{cardA, cardB}, p); len(pr) != 1 || !strings.Contains(pr[0], "summary") {
		t.Errorf("пустая summary: %v", pr)
	}
	one := &Plan{Writes: true, Files: []PlanFile{{Path: cardA}}}
	if pr := DoneProblems(Done{Changed: []string{cardA}, Summary: "x"}, []string{cardA}, one); len(pr) != 0 {
		t.Errorf("план из одного файла: %v", pr)
	}
	ro := &Plan{Writes: false, Files: []PlanFile{{Path: cardA}}}
	if pr := DoneProblems(Done{Summary: "x"}, []string{cardA}, ro); len(pr) != 2 {
		t.Errorf("без записи, а изменено: %v", pr)
	}
}

// --- приёмочные проверки этапа apply ------------------------------------

// runApply — план twoFiles (или свой), согласие -yes, выполнение по
// сценарию apply; возвращает прогон, сценарий и ошибку выполнения.
func runApply(t *testing.T, d project.Deps, plan Plan, apply []llm.Response, o Options) (*Run, *scripted, error) {
	t.Helper()
	ctx := context.Background()
	sc := &scripted{plan: []llm.Response{planCall(plan)}, apply: apply}
	fake := &llmtest.Fake{Fn: sc.fn}
	o.Model = "fake"
	r, err := New(ctx, d, "цель", o)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.DoPlan(ctx, fake, d, nil, o); err != nil {
		t.Fatal(err)
	}
	o.Yes = true
	if _, err := r.Approve(ctx, "", o); err != nil {
		t.Fatal(err)
	}
	err = r.DoApply(ctx, fake, d, nil, o)
	return r, sc, err
}

func TestAcceptanceContainsRejectsThenFixes(t *testing.T) {
	d, _ := initCourse(t)
	o := Options{Acceptance: []Check{{Kind: CheckContains, Path: cardA, Text: "## Итоги"}}}
	r, sc, err := runApply(t, d, twoFiles, []llm.Response{
		writeCall(cardA, "# 01\n\n## Выводы\n\nготово\n"),
		writeCall(cardB, "# 02\n\n## Выводы\n\nготово\n"),
		doneCall(Done{Changed: []string{cardA, cardB}, Summary: "готово"}),
		writeCall(cardA, "# 01\n\n## Итоги\n\nготово\n"),
		doneCall(Done{Changed: []string{cardA, cardB}, Summary: "готово, раздел Итоги добавлен"}),
	}, o)
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Report())
	}
	reply := llmtest.LastToolReply(sc.seen[4])
	if !strings.Contains(reply, "нет «## Итоги»") || !strings.Contains(reply, "добавь это в "+cardA) {
		t.Errorf("отказ приёмки: %s", reply)
	}
	if r.Rejected != 1 || r.Unverified || r.Stage != StageDone || r.Done.Summary != "готово, раздел Итоги добавлен" {
		t.Errorf("отказов %d, не проверено %v, этап %s, итог %q", r.Rejected, r.Unverified, r.Stage, r.Done.Summary)
	}
	if !strings.Contains(sc.seen[1].Messages[0].Content, "приёмочные проверки") {
		t.Error("в промпте apply нет фразы о приёмочных проверках")
	}
}

func TestAcceptanceFilesMin(t *testing.T) {
	d, _ := initCourse(t)
	r, sc, err := runApply(t, d, twoFiles, []llm.Response{
		writeCall(cardA, "# 01\n\nготово\n"),
		doneCall(Done{Changed: []string{cardA}, Summary: "готово"}),
		writeCall(cardB, "# 02\n\nготово\n"),
		doneCall(Done{Changed: []string{cardA, cardB}, Summary: "готово"}),
	}, Options{FilesMin: 2})
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Report())
	}
	reply := llmtest.LastToolReply(sc.seen[3])
	if !strings.Contains(reply, "внесите правки в остальные файлы плана: "+cardB) || !strings.Contains(reply, "нужно не меньше 2") {
		t.Errorf("отказ files_min: %s", reply)
	}
	if r.Rejected != 1 || r.Unverified || len(r.Changed) != 2 {
		t.Errorf("отказов %d, не проверено %v, изменено %v", r.Rejected, r.Unverified, r.Changed)
	}
}

func TestAcceptanceQuotesVerbatim(t *testing.T) {
	d, dir := initCourse(t)
	const chron = "docs/changelog-week1.md"
	plan := Plan{Goal: "хроника", Writes: true, Steps: []string{"цитаты"},
		Files: []PlanFile{{Path: chron, New: true, Why: "хроника"}, {Path: cardB, Why: "пометка"}}}
	check := Check{Kind: CheckQuotesVerbatim, Path: chron, Source: "docs/cards/*.md"}
	verbatim := "Модель — функция без состояния: весь контекст идёт в каждом запросе."
	paraphrase := "Модель — функция без состояния, весь контекст передаётся в каждом запросе"
	r, sc, err := runApply(t, d, plan, []llm.Response{
		writeCall(chron, "# Хроника\n\n## 01\n\n- «"+paraphrase+"»\n"),
		writeCall(cardB, "# 02\n\nготово\n"),
		doneCall(Done{Changed: []string{chron, cardB}, Summary: "готово"}),
		// Та же цитата другим регистром и с лишними пробелами — дословна после corpus.Normalize.
		writeCall(chron, "# Хроника\n\n## 01\n\n- «"+strings.ToUpper(verbatim[:12])+verbatim[12:]+"»\n"),
		doneCall(Done{Changed: []string{chron, cardB}, Summary: "готово"}),
	}, Options{Acceptance: []Check{check}})
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Report())
	}
	reply := llmtest.LastToolReply(sc.seen[4])
	if !strings.Contains(reply, "не дословны") || !strings.Contains(reply, "docs/cards/01-hello-go.md") || !strings.Contains(reply, "весь контекст идёт в каждом запросе") {
		t.Errorf("отказ по цитатам: %s", reply)
	}
	if r.Rejected != 1 || r.Unverified {
		t.Errorf("отказов %d, не проверено %v", r.Rejected, r.Unverified)
	}
	// NonVerbatimQuotes напрямую: ближайший фрагмент и «не найдена».
	wt := r.Worktree
	if err := os.WriteFile(filepath.Join(wt, filepath.FromSlash(chron)), []byte("- «"+paraphrase+"»\n- «совсем другая фраза про котов и собак на даче»\n- «кор»\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := NonVerbatimQuotes(wt, check)
	if len(bad) != 2 || bad[0].Source != "docs/cards/01-hello-go.md" || bad[0].Nearest != verbatim || bad[1].Nearest != "" {
		t.Errorf("недословные: %+v", bad)
	}
	if n := len(NonVerbatimQuotes(dir, Check{Kind: CheckQuotesVerbatim, Path: "docs/nope.md", Source: "docs/cards/*.md"})); n != 0 {
		t.Error("файла нет — пусто")
	}
}

func TestAcceptanceUnverifiedAfterRejects(t *testing.T) {
	d, _ := initCourse(t)
	o := Options{Acceptance: []Check{{Kind: CheckExists, Path: "docs/checks/never.md"}, {Kind: CheckCoverage, Pattern: "chunk_id", Source: "docs/cards/*.md"}}}
	done := doneCall(Done{Changed: []string{cardA, cardB}, Summary: "готово"})
	r, sc, err := runApply(t, d, twoFiles, []llm.Response{
		writeCall(cardA, "# 01\n\nготово\n"), writeCall(cardB, "# 02\n\nготово\n"), done, done, done,
	}, o)
	if err != nil {
		t.Fatalf("%v\n%s", err, r.Report())
	}
	reply := llmtest.LastToolReply(sc.seen[4])
	if !strings.Contains(reply, "файла docs/checks/never.md нет — создай") || !strings.Contains(reply, "упомянуты не все файлы") {
		t.Errorf("отказ: %s", reply)
	}
	if r.Rejected != 2 || !r.Unverified || r.Stage != StageDone || len(r.Problems()) != 2 {
		t.Errorf("отказов %d, не проверено %v, этап %s, проблемы %v", r.Rejected, r.Unverified, r.Stage, r.Problems())
	}
	if !strings.Contains(r.Report(), "не проверено") {
		t.Error("в отчёте нет пометки")
	}
}

func TestRunScriptPassesAcceptance(t *testing.T) {
	d, _ := initCourse(t)
	sc := &scripted{
		plan: []llm.Response{planCall(twoFiles)},
		apply: []llm.Response{
			writeCall(cardA, "# 01\n\nготово\n"),
			doneCall(Done{Changed: []string{cardA}, Summary: "готово"}),
			writeCall(cardB, "# 02\n\n## Итоги\n\nготово\n"),
			doneCall(Done{Changed: []string{cardA, cardB}, Summary: "готово"}),
		},
	}
	s := Script{ID: "T2", Goal: "допиши", Expect: Expect{FilesMin: 2, Writes: true,
		Checks: []Check{{Kind: CheckContains, Path: cardB, Text: "## Итоги"}}}}
	r, results, err := RunScript(context.Background(), &llmtest.Fake{Fn: sc.fn}, d, s, nil, Options{Model: "fake"})
	if err != nil {
		t.Fatal(err)
	}
	if r.Rejected != 1 || !AllOK(results) || len(r.Changed) != 2 {
		t.Errorf("отказов %d, проверки %v, изменено %v", r.Rejected, AllOK(results), r.Changed)
	}
	if reply := llmtest.LastToolReply(sc.seen[3]); !strings.Contains(reply, "нет «## Итоги»") {
		t.Errorf("приёмка из сценария не дошла до финишёра: %s", reply)
	}
}
