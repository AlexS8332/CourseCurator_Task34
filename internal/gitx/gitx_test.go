package gitx

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// newRepo — настоящий репозиторий во временном каталоге с одним
// коммитом; автор задаётся флагами -c, глобальный git config не трогаем.
func newRepo(t *testing.T) Repo {
	t.Helper()
	if err := Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Проба\n\nстрока\n"), 0o644)
	run(t, dir, "add", "README.md")
	run(t, dir, "commit", "-q", "-m", "первый коммит")
	return Repo{Dir: dir}
}

func run(t *testing.T, dir string, args ...string) string {
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

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestBranchStatusLog(t *testing.T) {
	g := newRepo(t)
	ctx := ctxT(t)
	if b, err := g.Branch(ctx); err != nil || b != "main" {
		t.Fatalf("ветка %q, %v", b, err)
	}
	if st, err := g.Status(ctx); err != nil || len(st) != 0 {
		t.Fatalf("чистое дерево: %v %v", st, err)
	}
	head, err := g.Head(ctx)
	if err != nil || len(head) != 40 {
		t.Fatalf("head %q %v", head, err)
	}
	tree, err := g.Tree(ctx)
	if err != nil || len(tree) != 40 {
		t.Fatalf("tree %q %v", tree, err)
	}
	os.WriteFile(filepath.Join(g.Dir, "new.txt"), []byte("x"), 0o644)
	st, err := g.Status(ctx)
	if err != nil || len(st) != 1 || !strings.HasPrefix(st[0], "??") {
		t.Fatalf("новый файл в статусе: %v %v", st, err)
	}
	if tree2, _ := g.Tree(ctx); !strings.HasSuffix(tree2, "-dirty") {
		t.Fatalf("грязное дерево: %s", tree2)
	}
	cs, err := g.Log(ctx, 5)
	if err != nil || len(cs) != 1 || cs[0].Subject != "первый коммит" || cs[0].Author != "Тест" || cs[0].Hash != head {
		t.Fatalf("журнал: %+v %v", cs, err)
	}
}

func TestDiffAndChangedFiles(t *testing.T) {
	g := newRepo(t)
	ctx := ctxT(t)
	os.WriteFile(filepath.Join(g.Dir, "README.md"), []byte("# Проба\n\nдругая строка\n"), 0o644)
	os.WriteFile(filepath.Join(g.Dir, "new.txt"), []byte("x"), 0o644)

	diff, truncated, err := g.Diff(ctx, "", 0)
	if err != nil || truncated || !strings.Contains(diff, "-строка") || !strings.Contains(diff, "+другая строка") {
		t.Fatalf("diff: %v %v\n%s", truncated, err, diff)
	}
	short, truncated, err := g.Diff(ctx, "", 40)
	if err != nil || !truncated || len(short) > 40 || !strings.HasSuffix(short, "\n") {
		t.Fatalf("обрезка: %v %v %q", truncated, err, short)
	}
	files, err := g.ChangedFiles(ctx, "")
	if err != nil || strings.Join(files, ",") != "README.md,new.txt" {
		t.Fatalf("файлы: %v %v", files, err)
	}

	run(t, g.Dir, "add", "-A")
	run(t, g.Dir, "commit", "-q", "-m", "второй")
	files, err = g.ChangedFiles(ctx, "HEAD~1..HEAD")
	if err != nil || strings.Join(files, ",") != "README.md,new.txt" {
		t.Fatalf("диапазон: %v %v", files, err)
	}
	if _, _, err := g.Diff(ctx, "--output=x", 0); err == nil {
		t.Fatal("диапазон-флаг должен отклоняться")
	}
	cs, _ := g.Log(ctx, 1)
	if len(cs) != 1 || cs[0].Subject != "второй" {
		t.Fatalf("n=1: %+v", cs)
	}
}

func TestEmptyRepo(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	g := Repo{Dir: dir}
	ctx := ctxT(t)
	if b, err := g.Branch(ctx); err != nil || b != "main" {
		t.Fatalf("ветка пустого репозитория: %q %v", b, err)
	}
	if head, err := g.Head(ctx); err != nil || head != "" {
		t.Fatalf("head: %q %v", head, err)
	}
	if cs, err := g.Log(ctx, 3); err != nil || len(cs) != 0 {
		t.Fatalf("log: %v %v", cs, err)
	}
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644)
	if files, err := g.ChangedFiles(ctx, ""); err != nil || strings.Join(files, ",") != "a.txt" {
		t.Fatalf("файлы: %v %v", files, err)
	}
	if _, _, err := g.Diff(ctx, "", 0); err != nil {
		t.Fatalf("diff без HEAD: %v", err)
	}
}

func TestNotRepo(t *testing.T) {
	if err := Available(); err != nil {
		t.Skip(err)
	}
	g := Repo{Dir: t.TempDir()}
	if g.IsRepo(ctxT(t)) {
		t.Fatal("временный каталог — не репозиторий")
	}
	if _, err := g.Branch(ctxT(t)); err == nil {
		t.Fatal("ждал ошибку")
	}
}

func TestGuard(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "docs", "cards"), 0o755)
	os.MkdirAll(filepath.Join(root, "assignments", "01-a", ".git"), 0o755)
	os.WriteFile(filepath.Join(root, "docs", "cards", "01-a.md"), []byte("x"), 0o644)
	g := Guard{Root: root}
	ok := []string{"docs/cards/01-a.md", `docs\cards\01-a.md`, "README.md", "assignments/01-a/README.md", filepath.Join(root, "docs")}
	for _, p := range ok {
		if _, err := g.Resolve(p); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	bad := []string{"", "../x", "docs/../../x", ".git/config", "assignments/01-a/.git/HEAD", ".env", ".env.local",
		"assignments/01-a/.env.local", "assignments/01-a/keys.local", "INDEX.md", "docs/registry.json", filepath.Join(filepath.Dir(root), "other")}
	for _, p := range bad {
		if abs, err := g.Resolve(p); err == nil {
			t.Errorf("%q прошёл оградой: %s", p, abs)
		}
	}
	if runtime.GOOS != "windows" {
		// Символическая ссылка наружу (на Windows нужны права — не проверяем).
		os.Symlink(filepath.Dir(root), filepath.Join(root, "out"))
		if _, err := g.Resolve("out/x"); err == nil {
			t.Error("ссылка наружу прошла")
		}
	}
	custom := Guard{Root: root, Deny: []string{"docs/cards/*"}}
	if _, err := custom.Resolve("docs/cards/01-a.md"); err == nil {
		t.Error("свой Deny не сработал")
	}
	if _, err := custom.Resolve("INDEX.md"); err != nil {
		t.Error("свой Deny заменяет умолчания, INDEX.md разрешён")
	}
}
