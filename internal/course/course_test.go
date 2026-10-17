package course

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testdata — мини-курс tools/curator/testdata/course.
func testdata(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "course"))
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestOpenFindsRootUpwards(t *testing.T) {
	root := testdata(t)
	r, err := Open(filepath.Join(root, "docs", "cards"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Dir != root {
		t.Fatalf("Dir = %s, ждал %s", r.Dir, root)
	}
	if _, err := Open(t.TempDir()); err == nil {
		t.Fatal("пустой каталог не должен открываться как курс")
	}
}

func TestAssignments(t *testing.T) {
	r := &Root{Dir: testdata(t)}
	all, err := r.Assignments()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("заданий %d, ждал 2: %+v", len(all), all)
	}
	a := all[1]
	if a.Num != "02" || a.Slug != "rag-basics" || a.Status != "in-progress" || a.Stack != "python" {
		t.Fatalf("02: %+v", a)
	}
	if a.DirName() != "02-rag-basics" || !filepath.IsAbs(a.Dir) {
		t.Fatalf("Dir = %s", a.Dir)
	}
	if len(a.Env) != 2 || a.Env[1] != "EMBED_BASE_URL" || len(a.Tags) != 1 {
		t.Fatalf("env/tags: %+v", a)
	}
}

func TestAssignmentsBadJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "assignments", "09-bad"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "assignments", "09-bad", "assignment.json"), []byte("{"), 0o644)
	if _, err := (&Root{Dir: dir}).Assignments(); err == nil || !strings.Contains(err.Error(), "09-bad") {
		t.Fatalf("ждал ошибку с путём, получил %v", err)
	}
}

func TestResolve(t *testing.T) {
	r := &Root{Dir: testdata(t)}
	for _, key := range []string{"01", "1", "hello-go", "01-hello-go", "hello"} {
		a, err := r.Assignment(key)
		if err != nil {
			t.Fatalf("%q: %v", key, err)
		}
		if a.Num != "01" {
			t.Fatalf("%q → %s", key, a.Num)
		}
	}
	if _, err := r.Assignment("-"); err == nil || !strings.Contains(err.Error(), "неоднозначно") {
		t.Fatalf("-: ждал неоднозначность (hello-go/rag-basics), получил %v", err)
	}
	if _, err := r.Assignment("99"); err == nil || !strings.Contains(err.Error(), "не найдено") {
		t.Fatalf("99: %v", err)
	}
	if _, err := r.Assignment(""); err == nil {
		t.Fatal("пустой ключ")
	}
}

func TestRegistryAndDrift(t *testing.T) {
	r := &Root{Dir: testdata(t)}
	reg, err := r.Registry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg) != 2 || reg[0].Num != "01" || !filepath.IsAbs(reg[0].Dir) {
		t.Fatalf("реестр: %+v", reg)
	}
	drift, err := r.Drift()
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 1 || !strings.Contains(drift[0], "02-rag-basics") || !strings.Contains(drift[0], "status") {
		t.Fatalf("drift = %v, ждал одно расхождение по статусу 02", drift)
	}
}

func TestDriftMissingRegistry(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "assignments", "01-a"), 0o755)
	os.WriteFile(filepath.Join(dir, "assignments", "01-a", "assignment.json"), []byte(`{"num":"01","slug":"a"}`), 0o644)
	drift, err := (&Root{Dir: dir}).Drift()
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) != 1 || !strings.Contains(drift[0], "нет в docs/registry.json") {
		t.Fatalf("drift = %v", drift)
	}
}

func TestLastRun(t *testing.T) {
	r := &Root{Dir: testdata(t)}
	rep, ok, err := r.LastRun("02")
	if err != nil || !ok {
		t.Fatalf("LastRun: ok=%v err=%v", ok, err)
	}
	if filepath.Base(rep.Dir) != "20260101-120500" {
		t.Fatalf("взял каталог %s, а не последний без -check", rep.Dir)
	}
	if rep.Passed || rep.Checks != 2 || rep.ChecksOK != 1 || rep.VideoSeconds != 42.5 || rep.Frames != 120 {
		t.Fatalf("отчёт: %+v", rep)
	}
	if rep.Failed == nil || rep.Failed.N != 3 || !strings.Contains(rep.Failed.Error, ".hit") {
		t.Fatalf("упавший шаг: %+v", rep.Failed)
	}
	if len(rep.Steps) != 3 || len(rep.JSErrors) != 1 {
		t.Fatalf("шаги %d, js %d", len(rep.Steps), len(rep.JSErrors))
	}
	if _, ok, err := r.LastRun("01"); err != nil || ok {
		t.Fatalf("у 01 прогонов нет: ok=%v err=%v", ok, err)
	}
	if _, _, err := r.LastRun("99"); err == nil {
		t.Fatal("99 — нет такого задания")
	}
}

func TestScenario(t *testing.T) {
	r := &Root{Dir: testdata(t)}
	a, _ := r.Assignment("02")
	sc, ok, err := r.Scenario(a)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if sc.Steps != 3 || !strings.Contains(sc.Title, "RAG") {
		t.Fatalf("%+v", sc)
	}
	a1, _ := r.Assignment("01")
	if _, ok, _ := r.Scenario(a1); ok {
		t.Fatal("у 01 сценария нет")
	}
}
