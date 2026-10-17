package project

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/ps"
	"aitr/34-files-agent/internal/tools"
)

// newCourse — копия мини-курса во временном каталоге: корень и задание
// 01 — настоящие репозитории git, плюс .env с ключом, который не должен
// утекать через инструменты.
func newCourse(t *testing.T) Deps {
	t.Helper()
	if err := gitx.Available(); err != nil {
		t.Skip(err)
	}
	src, err := filepath.Abs(filepath.Join("..", "..", "testdata", "course"))
	if err != nil {
		t.Fatal(err)
	}
	dst := t.TempDir()
	if err := copyDir(src, dst); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dst, ".env"), []byte("DEEPSEEK_API_KEY=secret-value-must-not-leak\n"), 0o644)
	os.WriteFile(filepath.Join(dst, "assignments", "02-rag-basics", ".env.local"), []byte("EMBED_BASE_URL=http://secret-host\n"), 0o644)
	os.WriteFile(filepath.Join(dst, ".gitignore"), []byte(".env\n*.local\nassignments/\n"), 0o644)
	git(t, dst, "init", "-q", "-b", "main")
	git(t, dst, "add", "-A")
	git(t, dst, "commit", "-q", "-m", "корень курса")
	a1 := filepath.Join(dst, "assignments", "01-hello-go")
	git(t, a1, "init", "-q", "-b", "task")
	git(t, a1, "add", "-A")
	git(t, a1, "commit", "-q", "-m", "задание 01")
	root, err := course.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	return Deps{Root: root, PS: ps.Fake{"doctor 01": "doctor: 01-hello-go\n  ok    stack = go\n  warn  origin не настроен\n  FAIL  нет go.mod\n"}}
}

func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "user.name=Тест", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if e.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
}

// call вызывает инструмент по имени и разбирает JSON-ответ.
func call(t *testing.T, d Deps, name, args string) map[string]any {
	t.Helper()
	out, err := callErr(d, name, args)
	if err != nil {
		t.Fatalf("%s(%s): %v", name, args, err)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(out), &m); err != nil {
		t.Fatalf("%s: не JSON: %v\n%s", name, err, out)
	}
	return m
}

func callErr(d Deps, name, args string) (string, error) {
	for _, tl := range Tools(d) {
		if tl.Spec().Name == name {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			return tl.Call(ctx, json.RawMessage(args))
		}
	}
	return "", errors.New("нет инструмента " + name)
}

func names(ts []tools.Tool) string { return strings.Join(tools.Names(ts), ",") }

func TestToolsAndRoles(t *testing.T) {
	d := Deps{Root: &course.Root{Dir: t.TempDir()}}
	all := Tools(d)
	want := "git_branch,git_status,git_log,git_diff,git_changed_files,repo_tree,read_file,repo_search,assignments_list,assignment_get,demo_report,aitr_doctor"
	if names(all) != want {
		t.Fatalf("без docs_search: %s", names(all))
	}
	if names(Role(all, "course")) != want {
		t.Fatalf("роль course без базы: %s", names(Role(all, "course")))
	}
	d.DocsSearch = tools.Func{S: tools.Spec{Name: DocsSearch, Description: "поиск", Parameters: json.RawMessage(`{}`), Untrusted: true}}
	all = Tools(d)
	if names(Role(all, "course")) != want+",docs_search" {
		t.Fatalf("с docs_search: %s", names(Role(all, "course")))
	}
	if len(Role(all, "nope")) != 0 {
		t.Fatal("неизвестная роль — пусто")
	}
	if names(Role(all, "crm")) != "docs_search" {
		t.Fatalf("crm: %s", names(Role(all, "crm")))
	}
	for _, tl := range all {
		s := tl.Spec()
		if s.Write {
			t.Errorf("%s: роль course только читает", s.Name)
		}
		if s.Description == "" || !json.Valid(s.Parameters) {
			t.Errorf("%s: пустое описание или битая схема", s.Name)
		}
		untrusted := map[string]bool{GitDiff: true, ReadFile: true, RepoSearch: true, AitrDoctor: true, DocsSearch: true}
		if s.Untrusted != untrusted[s.Name] {
			t.Errorf("%s: Untrusted = %v", s.Name, s.Untrusted)
		}
	}
	if _, err := tools.NewRegistry(all...); err != nil {
		t.Fatal(err)
	}
}

func TestGitTools(t *testing.T) {
	d := newCourse(t)
	if r := call(t, d, GitBranch, `{}`); r["branch"] != "main" || r["repo"] != "root" || len(r["head"].(string)) != 40 {
		t.Fatalf("корень: %v", r)
	}
	if r := call(t, d, GitBranch, `{"repo": "01"}`); r["branch"] != "task" || r["repo"] != "01-hello-go" {
		t.Fatalf("задание: %v", r)
	}
	if _, err := callErr(d, GitBranch, `{"repo": "99"}`); err == nil || !strings.Contains(err.Error(), "не найдено") {
		t.Fatalf("99: %v", err)
	}
	// 02 — каталог без git: ошибка словами.
	if _, err := callErr(d, GitBranch, `{"repo": "02"}`); err == nil || !strings.Contains(err.Error(), "не репозиторий") {
		t.Fatalf("02: %v", err)
	}
	if r := call(t, d, GitStatus, `{"repo": "root"}`); r["clean"] != true || r["changes"].(float64) != 0 {
		t.Fatalf("status: %v", r)
	}
	os.WriteFile(filepath.Join(d.Root.Dir, "docs", "course.md"), []byte("# Курс\n\nновое\n"), 0o644)
	os.WriteFile(filepath.Join(d.Root.Dir, "README.md"), []byte("# Мини-курс\n\nправка\n"), 0o644)
	r := call(t, d, GitStatus, `{}`)
	if r["clean"] != false || r["changes"].(float64) != 2 {
		t.Fatalf("после правки: %v", r)
	}
	r = call(t, d, GitChangedFiles, `{}`)
	if files := r["files"].([]any); len(files) != 2 || files[0] != "README.md" || files[1] != "docs/course.md" {
		t.Fatalf("changed: %v", r)
	}
	r = call(t, d, GitDiff, `{}`)
	if diff := r["diff"].(string); !strings.Contains(diff, "+правка") || r["truncated"] != false {
		t.Fatalf("diff: %v", r)
	}
	r = call(t, d, GitDiff, `{"max": 1024, "range": ""}`)
	if r["bytes"].(float64) > 1024 {
		t.Fatalf("предел: %v", r["bytes"])
	}
	r = call(t, d, GitLog, `{"n": 1}`)
	if cs := r["commits"].([]any); len(cs) != 1 || cs[0].(map[string]any)["subject"] != "корень курса" || cs[0].(map[string]any)["author"] != "Тест" {
		t.Fatalf("log: %v", r)
	}
}

func TestRepoTree(t *testing.T) {
	d := newCourse(t)
	r := call(t, d, RepoTree, `{"glob": "docs/cards/*.md"}`)
	if r["count"].(float64) != 4 {
		t.Fatalf("карточки: %v", r)
	}
	r = call(t, d, RepoTree, `{"glob": "*.json"}`)
	paths := map[string]bool{}
	for _, f := range r["files"].([]any) {
		paths[f.(map[string]any)["path"].(string)] = true
	}
	if !paths["docs/registry.json"] || !paths["assignments/01-hello-go/assignment.json"] || !paths["demos/02-rag-basics/scenario.json"] {
		t.Fatalf("json: %v", paths)
	}
	if paths["demos/02-rag-basics/runs/20260101-120500/report.json"] {
		t.Fatal("записи прогонов не показываются")
	}
	r = call(t, d, RepoTree, `{}`)
	for _, f := range r["files"].([]any) {
		if p := f.(map[string]any)["path"].(string); strings.Contains(p, ".git/") {
			t.Fatalf(".git в дереве: %s", p)
		}
	}
	if r = call(t, d, RepoTree, `{"max": 3}`); r["count"].(float64) != 3 || r["truncated"] != true {
		t.Fatalf("max: %v", r)
	}
	r = call(t, d, RepoTree, `{"glob": "**/README.md"}`)
	if r["count"].(float64) < 3 {
		t.Fatalf("**: %v", r)
	}
}

func TestReadFile(t *testing.T) {
	d := newCourse(t)
	r := call(t, d, ReadFile, `{"path": "docs/cards/01-hello-go.md"}`)
	if r["lines"].(float64) < 20 || !strings.HasPrefix(r["text"].(string), "# 01 — Первый запрос") || r["truncated"] != false {
		t.Fatalf("карточка: %v", r)
	}
	r = call(t, d, ReadFile, `{"path": "docs\\cards\\01-hello-go.md", "from": 9, "to": 10}`)
	if r["from"].(float64) != 9 || r["to"].(float64) != 10 || !strings.Contains(r["text"].(string), "## Цель") {
		t.Fatalf("диапазон: %v", r)
	}
	for _, p := range []string{".env", "assignments/02-rag-basics/.env.local", "INDEX.md", "docs/registry.json", ".git/HEAD", "assignments/01-hello-go/.git/HEAD", "../x"} {
		out, err := callErr(d, ReadFile, `{"path": "`+p+`"}`)
		if err == nil || strings.Contains(out, "secret") {
			t.Errorf("%s прочитался: %v %q", p, err, out)
		}
	}
	if _, err := callErr(d, ReadFile, `{"path": "docs/nope.md"}`); err == nil || !strings.Contains(err.Error(), "нет") {
		t.Fatalf("нет файла: %v", err)
	}
	if _, err := callErr(d, ReadFile, `{"path": "docs"}`); err == nil || !strings.Contains(err.Error(), "каталог") {
		t.Fatalf("каталог: %v", err)
	}
	if _, err := callErr(d, ReadFile, `{"path": "assignments/01-hello-go/blob.bin"}`); err == nil || !strings.Contains(err.Error(), "не текстовый") {
		t.Fatalf("бинарный: %v", err)
	}
	// Длинный файл режется по пределу.
	big := strings.Repeat("строка из текста для проверки предела чтения\n", 3000)
	os.WriteFile(filepath.Join(d.Root.Dir, "docs", "big.md"), []byte(big), 0o644)
	r = call(t, d, ReadFile, `{"path": "docs/big.md"}`)
	if r["truncated"] != true || len(r["text"].(string)) > ReadMax || r["to"].(float64) >= 3000 {
		t.Fatalf("предел: truncated=%v len=%d to=%v", r["truncated"], len(r["text"].(string)), r["to"])
	}
}

func TestRepoSearch(t *testing.T) {
	d := newCourse(t)
	r := call(t, d, RepoSearch, `{"q": "права этапа"}`)
	if r["count"].(float64) != 1 {
		t.Fatalf("подстрока: %v", r)
	}
	m := r["matches"].([]any)[0].(map[string]any)
	if m["path"] != "docs/cards/03-agent-tools.md" || m["line"].(float64) != 15 {
		t.Fatalf("совпадение: %v", m)
	}
	if r = call(t, d, RepoSearch, `{"q": "ЧАНК", "glob": "docs/notes/*.md"}`); r["count"].(float64) == 0 {
		t.Fatalf("без учёта регистра и с glob: %v", r)
	}
	if r = call(t, d, RepoSearch, `{"q": "^## (Цель|Выводы)$", "regexp": true, "glob": "*.md"}`); r["count"].(float64) < 6 {
		t.Fatalf("regexp: %v", r)
	}
	if r = call(t, d, RepoSearch, `{"q": "secret"}`); r["count"].(float64) != 0 {
		t.Fatalf("ключи нашлись: %v", r)
	}
	if r = call(t, d, RepoSearch, `{"q": "##", "limit": 2}`); r["count"].(float64) != 2 || r["truncated"] != true {
		t.Fatalf("limit: %v", r)
	}
	if _, err := callErr(d, RepoSearch, `{"q": "(", "regexp": true}`); err == nil {
		t.Fatal("битый regexp")
	}
	if _, err := callErr(d, RepoSearch, `{}`); err == nil {
		t.Fatal("пустой q")
	}
}

func TestAssignmentsTools(t *testing.T) {
	d := newCourse(t)
	r := call(t, d, AssignmentsList, `{}`)
	if r["count"].(float64) != 2 || r["by_status"].(map[string]any)["done"].(float64) != 1 {
		t.Fatalf("список: %v", r)
	}
	if drift := r["drift"].([]any); len(drift) != 1 || !strings.Contains(drift[0].(string), "02-rag-basics") {
		t.Fatalf("drift: %v", drift)
	}
	r = call(t, d, AssignmentsList, `{"status": "in-progress"}`)
	if r["count"].(float64) != 1 || r["total"].(float64) != 2 || r["assignments"].([]any)[0].(map[string]any)["num"] != "02" {
		t.Fatalf("фильтр: %v", r)
	}
	r = call(t, d, AssignmentGet, `{"num": "rag-basics"}`)
	a := r["assignment"].(map[string]any)
	if a["dir"] != "assignments/02-rag-basics" || r["card"] != "docs/cards/02-rag-basics.md" || r["has_card"] != true || r["runs"].(float64) != 1 {
		t.Fatalf("get: %v", r)
	}
	if sc := r["scenario"].(map[string]any); sc["steps"].(float64) != 3 || sc["path"] != "demos/02-rag-basics/scenario.json" {
		t.Fatalf("сценарий: %v", sc)
	}
	if r = call(t, d, AssignmentGet, `{"num": "01"}`); r["scenario"] != nil || r["runs"].(float64) != 0 {
		t.Fatalf("01: %v", r)
	}
	if _, err := callErr(d, AssignmentGet, `{"num": "7"}`); err == nil {
		t.Fatal("07 нет")
	}
}

func TestDemoReport(t *testing.T) {
	d := newCourse(t)
	r := call(t, d, DemoReport, `{"num": "02"}`)
	if r["found"] != true {
		t.Fatalf("%v", r)
	}
	rep := r["report"].(map[string]any)
	if rep["passed"] != false || rep["checks"].(float64) != 2 || rep["video_seconds"].(float64) != 42.5 || rep["dir"] != "demos/02-rag-basics/runs/20260101-120500" {
		t.Fatalf("отчёт: %v", rep)
	}
	if f := rep["failed"].(map[string]any); f["n"].(float64) != 3 || !strings.Contains(f["error"].(string), ".hit") {
		t.Fatalf("упавший шаг: %v", f)
	}
	if r = call(t, d, DemoReport, `{"num": "01"}`); r["found"] != false {
		t.Fatalf("01: %v", r)
	}
}

func TestAitrDoctor(t *testing.T) {
	d := newCourse(t)
	rec := &ps.Recorder{Inner: d.PS}
	d.PS = rec
	ts := Tools(d)
	var doctor tools.Tool
	for _, tl := range ts {
		if tl.Spec().Name == AitrDoctor {
			doctor = tl
		}
	}
	ctx := context.Background()
	out, err := doctor.Call(ctx, json.RawMessage(`{"num": "hello-go"}`))
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	json.Unmarshal([]byte(out), &r)
	if r["ok"].(float64) != 1 || r["warn"].(float64) != 1 || r["fail"].(float64) != 1 || r["cached"] != false || r["command"] != "aitr doctor 01" {
		t.Fatalf("doctor: %v", r)
	}
	out, _ = doctor.Call(ctx, json.RawMessage(`{"num": "01"}`))
	json.Unmarshal([]byte(out), &r)
	if r["cached"] != true || len(rec.Calls) != 1 {
		t.Fatalf("кэш: cached=%v вызовов=%v", r["cached"], rec.Calls)
	}
	if _, err := doctor.Call(ctx, json.RawMessage(`{"num": "02"}`)); err == nil {
		t.Fatal("у Fake нет ответа для 02 — ошибка")
	}
	d.PS = nil
	if _, err := callErr(d, AitrDoctor, `{"num": "01"}`); err == nil || !strings.Contains(err.Error(), "недоступен") {
		t.Fatalf("без PS: %v", err)
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pat, rel string
		want     bool
	}{
		{"*.md", "docs/cards/01.md", true}, {"*.md", "docs/x.txt", false},
		{"docs/cards/*.md", "docs/cards/01.md", true}, {"docs/cards/*.md", "docs/cards/sub/01.md", false},
		{"docs/**", "docs/cards/sub/01.md", true}, {"**/*.go", "a/b/c.go", true}, {"**/*.go", "c.go", true},
		{"assignments/*/README.md", "assignments/01-x/README.md", true}, {"", "anything", true},
		{"DOCS/*.MD", "docs/a.md", true},
	}
	for _, c := range cases {
		g, err := CompileGlob(c.pat)
		if err != nil {
			t.Fatal(err)
		}
		if g.Match(c.rel) != c.want {
			t.Errorf("%q ~ %q: ждал %v", c.pat, c.rel, c.want)
		}
	}
}
