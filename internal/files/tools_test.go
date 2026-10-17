package files

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/tools"
)

// gitRun — git с тестовым автором (глобальный config не трогаем).
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Тест", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// testRoot — репозиторий-копия курса с README.md, docs/a.md, .gitignore
// (sandbox/) и одним коммитом.
func testRoot(t *testing.T) gitx.Repo {
	t.Helper()
	if err := gitx.Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Курс\n\nстрока\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "a.md"), []byte("# A\n\n## Раздел\n\nодин\nдва\nтри\n\n## Другой\n\nконец\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("sandbox/\n"), 0o644)
	gitRun(t, dir, "add", "-A")
	gitRun(t, dir, "commit", "-q", "-m", "база")
	return gitx.Repo{Dir: dir}
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// setup — корень, worktree агента и инструменты записи по плану.
func setup(t *testing.T, plan *Plan, deny []string) (root, wt gitx.Repo, ts map[string]tools.Tool, rec *WriteLog) {
	t.Helper()
	root = testRoot(t)
	var err error
	wt, err = gitx.AddWorktree(ctxT(t), root, filepath.Join(root.Dir, "sandbox", "agent", "r1"), "agent/r1")
	if err != nil {
		t.Fatal(err)
	}
	rec = &WriteLog{}
	rec.Approve()
	ts = map[string]tools.Tool{}
	for _, tool := range Tools(wt, gitx.Guard{Root: wt.Dir, Deny: deny}, plan, rec) {
		ts[tool.Spec().Name] = tool
	}
	return root, wt, ts, rec
}

func call(t *testing.T, tool tools.Tool, args string) (map[string]any, error) {
	t.Helper()
	out, err := tool.Call(ctxT(t), json.RawMessage(args))
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("ответ не JSON: %s", out)
	}
	return m, nil
}

func TestToolsSpecs(t *testing.T) {
	ts := Tools(gitx.Repo{}, gitx.Guard{}, nil, nil)
	if strings.Join(tools.Names(ts), ",") != strings.Join(WriteTools, ",") {
		t.Fatalf("имена: %v", tools.Names(ts))
	}
	for _, tool := range ts {
		s := tool.Spec()
		if !s.Write || s.Untrusted || !json.Valid(s.Parameters) || s.Description == "" {
			t.Errorf("%s: Write=%v Untrusted=%v params=%s", s.Name, s.Write, s.Untrusted, s.Parameters)
		}
	}
}

func TestWriteFileInPlan(t *testing.T) {
	plan := &Plan{Goal: "правка", Writes: true, Files: []PlanFile{
		{Path: "docs/a.md", Why: "правим"}, {Path: "docs/new/b.md", New: true, Why: "создаём"}}}
	root, wt, ts, rec := setup(t, plan, nil)

	res, err := call(t, ts[WriteFile], `{"path":"docs/a.md","content":"# A\n\nновый текст\n"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res["created"] != false || res["lines"] != float64(3) || res["path"] != "docs/a.md" {
		t.Fatalf("ответ: %v", res)
	}
	res, err = call(t, ts[WriteFile], `{"path":"./docs\\new\\b.md","content":"# B"}`)
	if err != nil {
		t.Fatal(err)
	}
	if res["created"] != true || res["lines"] != float64(1) || res["path"] != "docs/new/b.md" {
		t.Fatalf("новый файл: %v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(wt.Dir, "docs", "new", "b.md"))
	if string(raw) != "# B" {
		t.Fatalf("содержимое: %q", raw)
	}
	// Корень не тронут: запись только в worktree.
	raw, _ = os.ReadFile(filepath.Join(root.Dir, "docs", "a.md"))
	if strings.Contains(string(raw), "новый текст") {
		t.Fatal("правка попала в корень")
	}
	if _, err := os.Stat(filepath.Join(root.Dir, "docs", "new")); err == nil {
		t.Fatal("новый файл попал в корень")
	}
	files, _ := wt.Porcelain(ctxT(t))
	if strings.Join(files, ",") != "docs/a.md,docs/new/b.md" {
		t.Fatalf("porcelain: %v", files)
	}
	if rec.Total() != 2 || rec.Outside() != 0 || rec.Before() != 0 {
		t.Fatalf("счётчики: %d %d %d", rec.Total(), rec.Outside(), rec.Before())
	}
	if _, err := call(t, ts[WriteFile], `{"path":"docs","content":"x"}`); err == nil {
		t.Fatal("каталог вместо файла — должен отказать (docs в плане нет)")
	}
}

func TestWriteOutsidePlanRefused(t *testing.T) {
	plan := &Plan{Goal: "правка", Writes: true, Files: []PlanFile{{Path: "docs/a.md", Why: "правим"}}}
	root, wt, ts, rec := setup(t, plan, nil)
	_, err := call(t, ts[WriteFile], `{"path":"README.md","content":"взлом"}`)
	if !errors.Is(err, ErrOutsidePlan) || !strings.Contains(err.Error(), "перепланируй") || !strings.Contains(err.Error(), "docs/a.md") {
		t.Fatalf("ошибка вне плана: %v", err)
	}
	_, err = call(t, ts[ApplyPatch], `{"path":"README.md","unified":"--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-# Курс\n+# Взлом\n"}`)
	if !errors.Is(err, ErrOutsidePlan) {
		t.Fatalf("патч вне плана: %v", err)
	}
	for _, dir := range []string{root.Dir, wt.Dir} {
		raw, _ := os.ReadFile(filepath.Join(dir, "README.md"))
		if string(raw) != "# Курс\n\nстрока\n" {
			t.Fatalf("README изменён в %s: %q", dir, raw)
		}
	}
	if rec.Outside() != 2 || rec.Total() != 2 {
		t.Fatalf("вне плана: %d из %d", rec.Outside(), rec.Total())
	}
	calls := rec.Calls()
	if len(calls) != 2 || !calls[0].Outside || calls[0].Err == "" || calls[0].Path != "README.md" {
		t.Fatalf("журнал: %+v", calls)
	}
}

func TestGuardDeny(t *testing.T) {
	// План с запрещёнными путями (Validate такой не пропустит, но ограда
	// инструмента — отдельный рубеж).
	plan := &Plan{Goal: "x", Writes: true, Files: []PlanFile{
		{Path: ".env", New: true}, {Path: "docs/registry.json", New: true}, {Path: "../out.md", New: true}, {Path: "docs/a.md"}}}
	_, wt, ts, rec := setup(t, plan, DefaultDeny)
	for _, p := range []string{".env", "docs/registry.json", "../out.md"} {
		_, err := call(t, ts[WriteFile], `{"path":"`+p+`","content":"x"}`)
		if !errors.Is(err, gitx.ErrDenied) {
			t.Errorf("%s: ждал ErrDenied, получил %v", p, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wt.Dir, ".env")); err == nil {
		t.Fatal(".env записан")
	}
	if rec.Total() != 3 || rec.Outside() != 0 {
		t.Fatalf("ограда считается как попытка, но не «вне плана»: %d %d", rec.Total(), rec.Outside())
	}
	// Свой Deny: закрыт docs/a.md.
	_, _, ts2, _ := setup(t, plan, []string{"docs/a.md"})
	if _, err := call(t, ts2[WriteFile], `{"path":"docs/a.md","content":"x"}`); !errors.Is(err, gitx.ErrDenied) {
		t.Fatalf("свой Deny: %v", err)
	}
}

func TestApplyPatchTool(t *testing.T) {
	plan := &Plan{Goal: "x", Writes: true, Files: []PlanFile{{Path: "docs/a.md"}, {Path: "README.md"}}}
	_, wt, ts, rec := setup(t, plan, nil)
	good := "--- a/docs/a.md\n+++ b/docs/a.md\n@@ -3,5 +3,6 @@\n ## Раздел\n \n один\n-два\n+два с половиной\n+два и три четверти\n три\n"
	res, err := call(t, ts[ApplyPatch], `{"path":"docs/a.md","unified":`+jsonStr(good)+`}`)
	if err != nil {
		t.Fatal(err)
	}
	if res["applied"] != true || res["added"] != float64(2) || res["removed"] != float64(1) {
		t.Fatalf("ответ: %v", res)
	}
	raw, _ := os.ReadFile(filepath.Join(wt.Dir, "docs", "a.md"))
	if !strings.Contains(string(raw), "два с половиной\nдва и три четверти\nтри") {
		t.Fatalf("патч не лёг:\n%s", raw)
	}
	// Повтор: контекст уже не совпадает — git apply откажет с подсказкой.
	_, err = call(t, ts[ApplyPatch], `{"path":"docs/a.md","unified":`+jsonStr(good)+`}`)
	if err == nil || !strings.Contains(err.Error(), "git apply") {
		t.Fatalf("повтор патча: %v", err)
	}
	// Патч на два файла — отказ, даже если оба в плане.
	two := good + "--- a/README.md\n+++ b/README.md\n@@ -1 +1 @@\n-# Курс\n+# Другой\n"
	if _, err := call(t, ts[ApplyPatch], `{"path":"docs/a.md","unified":`+jsonStr(two)+`}`); err == nil || !strings.Contains(err.Error(), "один вызов — один файл") {
		t.Fatalf("два файла: %v", err)
	}
	// path не совпадает с файлом патча.
	if _, err := call(t, ts[ApplyPatch], `{"path":"README.md","unified":`+jsonStr(good)+`}`); err == nil {
		t.Fatal("path ≠ файл патча должен отклоняться")
	}
	if _, err := call(t, ts[ApplyPatch], `{"path":"docs/a.md","unified":"не diff"}`); err == nil {
		t.Fatal("не-diff должен отклоняться")
	}
	if _, err := call(t, ts[ApplyPatch], `{"path":"docs/a.md","unified":""}`); err == nil {
		t.Fatal("пустой unified должен отклоняться")
	}
	if rec.Total() != 6 {
		t.Fatalf("попыток: %d", rec.Total())
	}
}

func TestGitCommitTool(t *testing.T) {
	plan := &Plan{Goal: "x", Writes: true, Files: []PlanFile{{Path: "docs/a.md"}}}
	root, wt, ts, rec := setup(t, plan, nil)
	if _, err := call(t, ts[GitCommit], `{"message":"пусто"}`); err == nil {
		t.Fatal("нечего коммитить — должен отказать")
	}
	if _, err := call(t, ts[WriteFile], `{"path":"docs/a.md","content":"# A\n\nправка\n"}`); err != nil {
		t.Fatal(err)
	}
	res, err := call(t, ts[GitCommit], `{"message":"правка a.md"}`)
	if err != nil {
		t.Fatal(err)
	}
	sha, _ := res["sha"].(string)
	if len(sha) != 40 || res["short"] != sha[:8] {
		t.Fatalf("sha: %v", res)
	}
	if files, _ := res["files"].([]any); len(files) != 1 || files[0] != "docs/a.md" {
		t.Fatalf("files: %v", res["files"])
	}
	if dirty, _ := wt.Porcelain(ctxT(t)); len(dirty) != 0 {
		t.Fatalf("после коммита грязно: %v", dirty)
	}
	cs, _ := wt.Log(ctxT(t), 1)
	if len(cs) != 1 || cs[0].Subject != "правка a.md" || cs[0].Hash != sha {
		t.Fatalf("журнал: %+v", cs)
	}
	if b, _ := root.Branch(ctxT(t)); b != "main" {
		t.Fatalf("корень переключился: %s", b)
	}
	if head, _ := root.Head(ctxT(t)); head == sha {
		t.Fatal("коммит попал в корень")
	}
	if rec.Total() != 3 || rec.Outside() != 0 {
		t.Fatalf("счётчики: %d %d", rec.Total(), rec.Outside())
	}
}

func TestWriteBeforeStage(t *testing.T) {
	// Контрольная дорожка: инструменты выданы на plan — плана и согласия
	// ещё нет. Любой вызов отказан и посчитан «до согласия».
	root := testRoot(t)
	rec := &WriteLog{}
	ts := map[string]tools.Tool{}
	for _, tool := range Tools(gitx.Repo{}, gitx.Guard{}, nil, rec) {
		ts[tool.Spec().Name] = tool
	}
	for name, args := range map[string]string{
		WriteFile:  `{"path":"docs/a.md","content":"x"}`,
		ApplyPatch: `{"path":"docs/a.md","unified":"--- a/docs/a.md\n+++ b/docs/a.md\n@@ -1 +1 @@\n-# A\n+# B\n"}`,
		GitCommit:  `{"message":"x"}`,
	} {
		if _, err := call(t, ts[name], args); !errors.Is(err, ErrNoStage) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// И с worktree, но без плана — то же (план ещё не принят).
	wt, _ := gitx.AddWorktree(ctxT(t), root, filepath.Join(root.Dir, "sandbox", "agent", "r2"), "agent/r2")
	for _, tool := range Tools(wt, gitx.Guard{Root: wt.Dir}, nil, rec) {
		if _, err := tool.Call(ctxT(t), json.RawMessage(`{"path":"docs/a.md","content":"x","unified":"x","message":"x"}`)); !errors.Is(err, ErrNoStage) {
			t.Errorf("%s без плана: %v", tool.Spec().Name, err)
		}
	}
	if rec.Total() != 7 || rec.Before() != 7 || rec.Outside() != 0 {
		t.Fatalf("до согласия: total=%d before=%d outside=%d", rec.Total(), rec.Before(), rec.Outside())
	}
	rec.Approve()
	rec.Record(WriteFile, "docs/a.md", false, nil)
	if rec.Before() != 7 || rec.Total() != 8 || !rec.Approved() {
		t.Fatalf("после согласия: before=%d total=%d", rec.Before(), rec.Total())
	}
	if raw, _ := os.ReadFile(filepath.Join(wt.Dir, "docs", "a.md")); !strings.HasPrefix(string(raw), "# A") {
		t.Fatal("файл изменён до согласия")
	}
}

func TestWriteLogNilAndConcurrent(t *testing.T) {
	var nilLog *WriteLog
	nilLog.Approve()
	nilLog.Record("x", "y", true, errors.New("e"))
	if nilLog.Total() != 0 || nilLog.Outside() != 0 || nilLog.Before() != 0 || nilLog.Calls() != nil || nilLog.Approved() {
		t.Fatal("нулевой журнал должен быть пустым")
	}
	l := &WriteLog{}
	done := make(chan bool)
	for i := 0; i < 8; i++ {
		go func(i int) {
			for j := 0; j < 50; j++ {
				l.Record(WriteFile, "p", i%2 == 0, nil)
			}
			done <- true
		}(i)
	}
	for i := 0; i < 8; i++ {
		<-done
	}
	if l.Total() != 400 || l.Outside() != 200 || l.Before() != 400 {
		t.Fatalf("параллельно: %d %d %d", l.Total(), l.Outside(), l.Before())
	}
}

func TestPlanValidate(t *testing.T) {
	root := testRoot(t)
	o := Options{}
	ok := &Plan{Writes: true, Files: []PlanFile{{Path: "docs\\a.md"}, {Path: "./docs/new.md", New: true}}}
	if err := ok.Validate(root.Dir, o); err != nil {
		t.Fatal(err)
	}
	if ok.Files[0].Path != "docs/a.md" || ok.Files[1].Path != "docs/new.md" {
		t.Fatalf("пути не нормализованы: %+v", ok.Files)
	}
	if _, has := ok.Has("docs\\new.md"); !has {
		t.Fatal("Has не нашёл нормализованный путь")
	}
	bad := map[string]*Plan{
		"пусто":              {Writes: true},
		"нет файла":          {Writes: true, Files: []PlanFile{{Path: "docs/none.md"}}},
		"есть, но new":       {Writes: true, Files: []PlanFile{{Path: "docs/a.md", New: true}}},
		"new без writes":     {Files: []PlanFile{{Path: "docs/new.md", New: true}}},
		"вне allowed":        {Writes: true, Files: []PlanFile{{Path: "README.md"}}},
		"deny":               {Writes: true, Files: []PlanFile{{Path: "docs/registry.json", New: true}}},
		"каталог":            {Writes: true, Files: []PlanFile{{Path: "docs"}}},
		"повтор":             {Writes: true, Files: []PlanFile{{Path: "docs/a.md"}, {Path: "docs/a.md"}}},
		"наружу":             {Writes: true, Files: []PlanFile{{Path: "docs/../../x.md", New: true}}},
		"assignments в docs": {Writes: true, Files: []PlanFile{{Path: "docs/assignments/x.md", New: true}}},
	}
	for name, p := range bad {
		if err := p.Validate(root.Dir, o); !errors.Is(err, ErrPlan) {
			t.Errorf("%s: ждал ErrPlan, получил %v", name, err)
		}
	}
	// Свои Allowed/Deny.
	custom := Options{Allowed: []string{"README.md"}, Deny: []string{"docs/*"}}
	if err := (&Plan{Files: []PlanFile{{Path: "README.md"}}}).Validate(root.Dir, custom); err != nil {
		t.Fatal(err)
	}
	if err := (&Plan{Files: []PlanFile{{Path: "docs/a.md"}}}).Validate(root.Dir, custom); !errors.Is(err, ErrPlan) {
		t.Fatal("свой Allowed не сработал")
	}
	// Чтение без записи — только существующие файлы.
	if err := (&Plan{Files: []PlanFile{{Path: "docs/a.md"}}}).Validate(root.Dir, o); err != nil {
		t.Fatal(err)
	}
}

func TestAllowedAndNormPath(t *testing.T) {
	for p, want := range map[string]string{"./docs/a.md": "docs/a.md", "docs\\x\\..\\a.md": "docs/a.md", " /docs/a.md ": "docs/a.md", "docs/": "docs", "": ""} {
		if got := NormPath(p); got != want {
			t.Errorf("NormPath(%q) = %q, ждал %q", p, got, want)
		}
	}
	yes := []string{"docs/a.md", "docs/cards/x.md", ".claude/skills/aitr/SKILL.md", "tools/curator/go.mod", "templates/card.md"}
	no := []string{"docs", "README.md", "scripts/aitr.ps1", "assignments/01-a/README.md", "documents/a.md", "tools/x.md"}
	for _, p := range yes {
		if !Allowed(p, DefaultAllowed) {
			t.Errorf("%s должен быть разрешён", p)
		}
	}
	for _, p := range no {
		if Allowed(p, DefaultAllowed) {
			t.Errorf("%s не должен быть разрешён", p)
		}
	}
	if !Allowed("README.md", []string{"README.md"}) || !Allowed("docs/x/y.md", []string{"docs"}) || !Allowed("a/b.md", []string{"*/*.md"}) {
		t.Error("образцы без «/» в конце")
	}
	if len(NewID()) != 24 || NewID() == NewID() {
		t.Errorf("NewID: %q", NewID())
	}
}

func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
