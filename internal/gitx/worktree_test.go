package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorktreeLifecycle(t *testing.T) {
	root := newRepo(t)
	ctx := ctxT(t)
	// sandbox/ игнорируется, как в корне курса: worktree внутри дерева не
	// должен всплывать в статусе корня.
	os.WriteFile(filepath.Join(root.Dir, ".gitignore"), []byte("sandbox/\n"), 0o644)
	run(t, root.Dir, "add", ".gitignore")
	run(t, root.Dir, "commit", "-q", "-m", "gitignore")

	dir := filepath.Join(root.Dir, "sandbox", "agent", "r1")
	wt, err := AddWorktree(ctx, root, dir, "agent/r1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := wt.Branch(ctx); b != "agent/r1" {
		t.Fatalf("ветка worktree %q", b)
	}
	if b, _ := root.Branch(ctx); b != "main" {
		t.Fatalf("ветка корня изменилась: %q", b)
	}
	if _, err := os.Stat(filepath.Join(wt.Dir, "README.md")); err != nil {
		t.Fatalf("в worktree нет файлов HEAD: %v", err)
	}
	if files, _ := wt.Porcelain(ctx); len(files) != 0 {
		t.Fatalf("свежий worktree не чист: %v", files)
	}

	// Изменение, новый файл, файл с пробелом в имени.
	os.WriteFile(filepath.Join(wt.Dir, "README.md"), []byte("# Проба\n\nновая строка\n"), 0o644)
	os.MkdirAll(filepath.Join(wt.Dir, "docs", "sub"), 0o755)
	os.WriteFile(filepath.Join(wt.Dir, "docs", "sub", "new.md"), []byte("# Новый\n"), 0o644)
	os.WriteFile(filepath.Join(wt.Dir, "docs", "с пробелом.md"), []byte("x\n"), 0o644)
	files, err := wt.Porcelain(ctx)
	if err != nil || strings.Join(files, ",") != "README.md,docs/sub/new.md,docs/с пробелом.md" {
		t.Fatalf("porcelain: %v %v", files, err)
	}
	diff, truncated, err := wt.DiffAll(ctx, 0)
	if err != nil || truncated {
		t.Fatalf("diff: %v %v", truncated, err)
	}
	for _, want := range []string{"+новая строка", "new file mode", "+# Новый"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("в diff нет %q:\n%s", want, diff)
		}
	}
	if st, _ := root.Status(ctx); len(st) != 0 {
		t.Fatalf("статус корня испачкан worktree: %v", st)
	}
	// Diff worktree применим к базе (корню): так человек и сольёт.
	if err := root.ApplyCheck(ctx, diff); err != nil {
		t.Fatalf("apply --check к корню: %v", err)
	}
	// А к самому worktree — уже нет: изменения там есть.
	if err := wt.ApplyCheck(ctx, diff); err == nil {
		t.Fatal("apply --check к worktree должен отказать")
	}
	if err := wt.ApplyCheck(ctx, ""); err != nil {
		t.Fatalf("пустой патч применим: %v", err)
	}

	if err := wt.AddAll(ctx); err != nil {
		t.Fatal(err)
	}
	sha, err := wt.Commit(ctx, "правка агента")
	if err != nil || len(sha) != 40 {
		t.Fatalf("commit: %q %v", sha, err)
	}
	if files, _ := wt.Porcelain(ctx); len(files) != 0 {
		t.Fatalf("после коммита дерево грязное: %v", files)
	}
	cs, _ := wt.Log(ctx, 1)
	if len(cs) != 1 || cs[0].Subject != "правка агента" {
		t.Fatalf("журнал worktree: %+v", cs)
	}
	if _, err := wt.Commit(ctx, "пусто"); err == nil {
		t.Fatal("коммит без изменений должен отказать")
	}
	changed, _ := wt.ChangedFiles(ctx, "HEAD~1..HEAD")
	if strings.Join(changed, ",") != "README.md,docs/sub/new.md,docs/с пробелом.md" {
		t.Fatalf("изменённые в коммите: %v", changed)
	}
	// Ветка видна из корня, корень не тронут.
	if _, err := root.Run(ctx, "rev-parse", "--verify", "-q", "refs/heads/agent/r1"); err != nil {
		t.Fatalf("ветки agent/r1 нет в корне: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(root.Dir, "README.md")); strings.Contains(string(raw), "новая строка") {
		t.Fatal("правка попала в корень")
	}

	// Та же ветка второй раз — суффикс.
	dir2 := filepath.Join(root.Dir, "sandbox", "agent", "r1b")
	wt2, err := AddWorktree(ctx, root, dir2, "agent/r1")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := wt2.Branch(ctx); b != "agent/r1-2" {
		t.Fatalf("суффикс ветки: %q", b)
	}
	if _, err := AddWorktree(ctx, root, dir2, "agent/x"); err == nil {
		t.Fatal("занятый каталог должен отклоняться")
	}
	if _, err := AddWorktree(ctx, root, filepath.Join(root.Dir, "sandbox", "z"), "-bad"); err == nil {
		t.Fatal("имя-флаг должно отклоняться")
	}

	if err := RemoveWorktree(ctx, root, dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); err == nil {
		t.Fatal("каталог worktree остался")
	}
	out, _ := root.Run(ctx, "worktree", "list", "--porcelain")
	if strings.Contains(out, filepath.ToSlash(dir)+"\n") {
		t.Fatalf("worktree не убран из списка:\n%s", out)
	}
	if _, err := root.Run(ctx, "rev-parse", "--verify", "-q", "refs/heads/agent/r1"); err != nil {
		t.Fatal("ветка должна остаться после удаления worktree")
	}
	// Повторное удаление уже убранного — без ошибки (prune).
	if err := RemoveWorktree(ctx, root, dir); err != nil {
		t.Fatalf("повторное удаление: %v", err)
	}
}

func TestApplyPatch(t *testing.T) {
	g := newRepo(t)
	ctx := ctxT(t)
	patch := "--- a/README.md\n+++ b/README.md\n@@ -1,3 +1,3 @@\n # Проба\n \n-строка\n+другая строка\n"
	if err := g.ApplyCheck(ctx, patch); err != nil {
		t.Fatal(err)
	}
	if err := g.Apply(ctx, patch); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(g.Dir, "README.md"))
	if !strings.Contains(string(raw), "другая строка") {
		t.Fatalf("патч не применился:\n%s", raw)
	}
	if err := g.Apply(ctx, patch); err == nil {
		t.Fatal("второй раз патч не ложится")
	}
	if err := g.ApplyCheck(ctx, "мусор, не diff"); err == nil {
		t.Fatal("не-diff должен отклоняться")
	}
}

func TestCommitWithoutIdentity(t *testing.T) {
	g := newRepo(t)
	ctx := ctxT(t)
	os.WriteFile(filepath.Join(g.Dir, "a.txt"), []byte("a\n"), 0o644)
	if err := g.AddAll(ctx); err != nil {
		t.Fatal(err)
	}
	// Автор не настроен в репозитории: Commit подставляет curator (или
	// берёт глобального — тогда имя любое, лишь бы коммит состоялся).
	sha, err := g.Commit(ctx, "")
	if err != nil || sha == "" {
		t.Fatalf("commit без автора: %q %v", sha, err)
	}
	cs, _ := g.Log(ctx, 1)
	if len(cs) != 1 || cs[0].Subject != "изменения файлового агента" {
		t.Fatalf("сообщение по умолчанию: %+v", cs)
	}
}
