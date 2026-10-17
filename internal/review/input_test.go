package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/gitx"
)

// tempRepo — временный репозиторий с двумя коммитами: первый — a.txt и
// docs/b.md, второй — правка a.txt, новый c.ps1, удаление docs/b.md.
func tempRepo(t *testing.T) gitx.Repo {
	t.Helper()
	if err := gitx.Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	repo := gitx.Repo{Dir: dir}
	ctx := context.Background()
	run := func(args ...string) {
		t.Helper()
		if _, err := repo.Run(ctx, args...); err != nil {
			t.Fatal(err)
		}
	}
	run("init", "-q")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "test")
	run("config", "core.autocrlf", "false")
	write := func(rel, text string) {
		t.Helper()
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a.txt", "one\ntwo\nthree\n")
	write("docs/b.md", "# b\n")
	run("add", "-A")
	run("commit", "-q", "-m", "первый")
	write("a.txt", "one\ntwo changed\nthree\nfour\n")
	write("c.ps1", "git push --force\n")
	if err := os.Remove(filepath.Join(dir, "docs", "b.md")); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "второй: правка a, новый c, удалён b")
	return repo
}

func TestFromRange(t *testing.T) {
	repo := tempRepo(t)
	ctx := context.Background()

	in, err := FromRange(ctx, repo, "HEAD~1...HEAD", 0)
	if err != nil {
		t.Fatal(err)
	}
	if in.Range != "HEAD~1...HEAD" || in.Base != "HEAD~1" || in.Head != "HEAD" || in.Truncated || in.Number != 0 {
		t.Errorf("вход: %+v", in)
	}
	if !strings.HasPrefix(in.Title, "второй") {
		t.Errorf("заголовок — тема коммита: %q", in.Title)
	}
	if got := strings.Join(in.Paths(), ","); got != "a.txt,c.ps1,docs/b.md" {
		t.Errorf("файлы: %s", got)
	}
	a := in.File("a.txt")
	if a == nil || a.Status != StatusModified || a.Added != 2 || a.Removed != 1 || !a.Commentable(4) || !a.Commentable(2) {
		t.Errorf("a.txt: %+v", a)
	}
	if txt, ok := a.LineText(2); !ok || txt != "two changed" {
		t.Errorf("a.txt строка 2: %q", txt)
	}
	if c := in.File("c.ps1"); c == nil || c.Status != StatusAdded || !c.Commentable(1) {
		t.Errorf("c.ps1: %+v", c)
	}
	if b := in.File("docs/b.md"); b == nil || b.Status != StatusDeleted || b.Commentable(1) {
		t.Errorf("docs/b.md: %+v", b)
	}
	if !strings.Contains(in.Diff, "diff --git a/a.txt b/a.txt") {
		t.Errorf("Diff — сырой текст: %q", clip(in.Diff, 100))
	}

	// Обрезка: предел меньше diff — Truncated, хвостовые файлы без hunk'ов, но в списке.
	cut, err := FromRange(ctx, repo, "HEAD~1..HEAD", 120)
	if err != nil {
		t.Fatal(err)
	}
	if !cut.Truncated || len(cut.Diff) > 120 || len(cut.Files) != 3 {
		t.Errorf("обрезанный: truncated=%v, байт %d, файлов %d", cut.Truncated, len(cut.Diff), len(cut.Files))
	}
	if c := cut.File("c.ps1"); c == nil || len(c.Hunks) != 0 {
		t.Errorf("файл за пределом обрезки — без hunk'ов: %+v", c)
	}
	if cut.Base != "HEAD~1" || cut.Head != "HEAD" {
		t.Errorf("диапазон «..»: %+v", cut)
	}

	// Пустой диапазон — незакоммиченные изменения.
	if err := os.WriteFile(filepath.Join(repo.Dir, "a.txt"), []byte("one\ntwo changed\nthree\nfour\nfive\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt, err := FromRange(ctx, repo, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(wt.Files) != 1 || wt.Files[0].Path != "a.txt" || !wt.Files[0].Commentable(5) || wt.Head != "HEAD" || wt.Base != "" {
		t.Errorf("рабочее дерево: %+v", wt)
	}
	if wt.Title != "незакоммиченные изменения" {
		t.Errorf("заголовок: %q", wt.Title)
	}

	if _, err := FromRange(ctx, repo, "-x", 0); err == nil {
		t.Error("диапазон-флаг должен отвергаться")
	}
	if _, err := FromRange(ctx, repo, "nope...HEAD", 0); err == nil {
		t.Error("несуществующая ревизия — ошибка git")
	}
}

func TestFromPatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "P4.patch")
	if err := os.WriteFile(p, []byte("\uFEFF"+sampleDiff), 0o644); err != nil {
		t.Fatal(err)
	}
	in, err := FromPatch(p, "")
	if err != nil {
		t.Fatal(err)
	}
	if in.Title != "P4" || len(in.Files) != 5 || !strings.HasPrefix(in.Diff, "diff --git") || in.Range != "" {
		t.Errorf("FromPatch: title=%q files=%d", in.Title, len(in.Files))
	}
	if in, _ := FromPatch(p, "Заголовок"); in.Title != "Заголовок" {
		t.Errorf("заголовок: %q", in.Title)
	}
	empty := filepath.Join(dir, "empty.patch")
	os.WriteFile(empty, []byte("ничего\n"), 0o644)
	if _, err := FromPatch(empty, ""); err == nil {
		t.Error("патч без файлов — ошибка")
	}
	if _, err := FromPatch(filepath.Join(dir, "nope.patch"), ""); err == nil {
		t.Error("нет файла — ошибка")
	}
}
