package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/course"
)

func TestSnapshot(t *testing.T) {
	src := t.TempDir()
	write := func(rel, text string) {
		p := filepath.Join(src, filepath.FromSlash(rel))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(text), 0o644)
	}
	write("go.mod", "module example.com/fake\n\ngo 1.25.0\n")
	write("go.sum", "x\n")
	write("cmd/curator/main.go", "package main\n\nimport \"example.com/fake/internal/course\"\n\nvar _ = course.Open\n")
	write("internal/course/course.go", "package course\n\nfunc Open() {}\n")
	write("data/course.db", "db")
	write("curator.exe", "exe")
	write("testdata/course/CLAUDE.md", "# x\n")

	dst := filepath.Join(t.TempDir(), "curator")
	n, err := Snapshot(src, dst, "example.com/new")
	if err != nil {
		t.Fatal(err)
	}
	if n != 5 {
		t.Fatalf("скопировано %d, ждал 5 (без data/ и exe)", n)
	}
	gomod, _ := os.ReadFile(filepath.Join(dst, "go.mod"))
	if !strings.HasPrefix(string(gomod), "module example.com/new\n") || !strings.Contains(string(gomod), "go 1.25.0") {
		t.Fatalf("go.mod: %s", gomod)
	}
	main, _ := os.ReadFile(filepath.Join(dst, "cmd", "curator", "main.go"))
	if !strings.Contains(string(main), `"example.com/new/internal/course"`) {
		t.Fatalf("импорт не переписан: %s", main)
	}
	for _, p := range []string{"data/course.db", "curator.exe"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(p))); err == nil {
			t.Errorf("%s попал в снимок", p)
		}
	}
	for _, p := range []string{"go.sum", "testdata/course/CLAUDE.md", "internal/course/course.go"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(p))); err != nil {
			t.Errorf("%s не скопирован", p)
		}
	}
}

// openRootT — мини-курс testdata/course как корень.
func openRootT(t *testing.T) *course.Root {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "course"))
	if err != nil {
		t.Fatal(err)
	}
	r, err := openRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDataDir(t *testing.T) {
	root := openRootT(t)
	t.Setenv(EnvData, "")
	if got := dataDir(root, ""); got != filepath.Join(root.Dir, "tools", "curator", "data") {
		t.Fatalf("умолчание: %s", got)
	}
	if got := dataDir(root, "x"); got != "x" {
		t.Fatalf("флаг: %s", got)
	}
	t.Setenv(EnvData, "y")
	if got := dataDir(root, ""); got != "y" {
		t.Fatalf("переменная: %s", got)
	}
}
