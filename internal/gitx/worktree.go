package gitx

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Worktree (день 34): ветка agent/<run> в каталоге sandbox/agent/<run>,
// куда пишет файловый агент. Корневой репозиторий остаётся нетронутым,
// слияние — человек.

// AddWorktree — `git worktree add -b <branch> <dir> HEAD` в корневом
// репозитории root. Если ветка уже есть — к имени добавляется суффикс
// -2, -3, …; настоящее имя ветки узнаётся через (Repo).Branch у
// результата. Возвращает репозиторий worktree (Dir — абсолютный путь).
func AddWorktree(ctx context.Context, root Repo, dir, branch string) (Repo, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" || strings.HasPrefix(branch, "-") {
		return Repo{}, fmt.Errorf("имя ветки %q не годится", branch)
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Repo{}, err
	}
	if _, err := os.Stat(abs); err == nil {
		return Repo{}, fmt.Errorf("каталог %s уже есть", abs)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return Repo{}, err
	}
	name := branch
	for i := 2; i < 100; i++ {
		if _, err := root.Run(ctx, "rev-parse", "--verify", "-q", "refs/heads/"+name); err != nil {
			break // ветки нет — имя свободно
		}
		name = branch + "-" + strconv.Itoa(i)
	}
	if _, err := root.Run(ctx, "worktree", "add", "-q", "-b", name, abs, "HEAD"); err != nil {
		return Repo{}, err
	}
	return Repo{Dir: abs}, nil
}

// RemoveWorktree — убирает worktree в dir (`git worktree remove --force`)
// и чистит записи об исчезнувших (`prune`). Ветка остаётся: её diff
// нужен человеку для слияния.
func RemoveWorktree(ctx context.Context, root Repo, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if _, err := root.Run(ctx, "worktree", "remove", "--force", abs); err != nil {
		if _, statErr := os.Stat(abs); statErr == nil {
			return err // каталог на месте — убрать не вышло
		}
		// Каталога уже нет — достаточно prune.
	}
	_, err = root.Run(ctx, "worktree", "prune")
	return err
}

// Porcelain — пути изменённых и новых файлов по `git status --porcelain`
// (с «/», у переименованных — новый путь; пусто — дерево чистое).
func (g Repo) Porcelain(ctx context.Context) ([]string, error) {
	out, err := g.Run(ctx, "status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, l := range lines(out) {
		if len(l) < 4 {
			continue
		}
		p := l[3:]
		if i := strings.Index(p, " -> "); i >= 0 {
			p = p[i+4:]
		}
		if strings.HasPrefix(p, `"`) {
			if u, err := strconv.Unquote(p); err == nil {
				p = u
			}
		}
		files = append(files, filepath.ToSlash(p))
	}
	return files, nil
}

// AddAll — `git add -A`.
func (g Repo) AddAll(ctx context.Context) error {
	_, err := g.Run(ctx, "add", "-A")
	return err
}

// Commit — коммит проиндексированного с сообщением msg; возвращает sha.
// Если в репозитории не настроен автор, подставляется «curator» (только
// для этого вызова, глобальный config не трогается). Нечего коммитить —
// ошибка git.
func (g Repo) Commit(ctx context.Context, msg string) (string, error) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		msg = "изменения файлового агента"
	}
	var args []string
	if name, _ := g.Run(ctx, "config", "user.name"); strings.TrimSpace(name) == "" {
		args = append(args, "-c", "user.name=curator", "-c", "user.email=curator@localhost")
	}
	args = append(args, "-c", "commit.gpgsign=false", "commit", "-q", "-m", msg)
	if _, err := g.Run(ctx, args...); err != nil {
		return "", err
	}
	return g.Head(ctx)
}

// DiffAll — все незакоммиченные изменения относительно HEAD, включая
// новые файлы (они добавляются в индекс как intent-to-add, `git add -N`,
// чтобы попасть в diff целиком). Обрезка по max — как у Diff.
func (g Repo) DiffAll(ctx context.Context, max int) (string, bool, error) {
	untracked, err := g.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return "", false, err
	}
	if files := lines(untracked); len(files) > 0 {
		if _, err := g.Run(ctx, append([]string{"add", "-N", "--"}, files...)...); err != nil {
			return "", false, err
		}
	}
	return g.Diff(ctx, "", max)
}

// ApplyCheck — применим ли патч к рабочему дереву (`git apply --check`);
// пустой патч применим.
func (g Repo) ApplyCheck(ctx context.Context, patch string) error {
	return g.apply(ctx, patch, true)
}

// Apply — применяет unified diff к рабочему дереву (`git apply`).
func (g Repo) Apply(ctx context.Context, patch string) error {
	return g.apply(ctx, patch, false)
}

func (g Repo) apply(ctx context.Context, patch string, check bool) error {
	if strings.TrimSpace(patch) == "" {
		return nil
	}
	if !strings.HasSuffix(patch, "\n") {
		patch += "\n"
	}
	f, err := os.CreateTemp("", "curator-*.patch")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(patch); err != nil {
		f.Close()
		return err
	}
	f.Close()
	args := []string{"apply", "--whitespace=nowarn"}
	if check {
		args = append(args, "--check")
	}
	_, err = g.Run(ctx, append(args, f.Name())...)
	return err
}
