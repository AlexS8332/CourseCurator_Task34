// Package gitx — git-репозиторий для ассистента: ветка, состояние, журнал,
// diff и список изменённых файлов. Всё через команду git (она есть на
// любой машине курса), без библиотек: вывод тот же, что видит человек.
//
// Worktree (ветка agent/<run> в sandbox/) — день 34, здесь его нет.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// Repo — репозиторий в каталоге Dir (им может быть и подкаталог).
type Repo struct{ Dir string }

// Commit — строка журнала.
type Commit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Author  string `json:"author"`
	Date    string `json:"date"` // ISO 8601 (git %aI)
	Subject string `json:"subject"`
}

// MaxLog — предел n у Log: журнал длиннее модели не нужен.
const MaxLog = 50

// Run выполняет git с аргументами в каталоге репозитория и возвращает
// stdout. Ошибка git — с текстом stderr (он у git и есть объяснение).
func (g Repo) Run(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.quotepath=off"}, args...)...)
	cmd.Dir = g.Dir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), msg)
	}
	return out.String(), nil
}

// IsRepo — есть ли в Dir репозиторий (или он внутри репозитория).
func (g Repo) IsRepo(ctx context.Context) bool {
	_, err := g.Run(ctx, "rev-parse", "--git-dir")
	return err == nil
}

// Toplevel — корень рабочего дерева, в котором лежит Dir.
func (g Repo) Toplevel(ctx context.Context) (string, error) {
	out, err := g.Run(ctx, "rev-parse", "--show-toplevel")
	return strings.TrimSpace(out), err
}

// Branch — текущая ветка. У отсоединённой головы — «HEAD (detached at
// <короткий хеш>)»; у репозитория без коммитов — имя будущей ветки.
func (g Repo) Branch(ctx context.Context) (string, error) {
	out, err := g.Run(ctx, "symbolic-ref", "--short", "-q", "HEAD")
	if err == nil && strings.TrimSpace(out) != "" {
		return strings.TrimSpace(out), nil
	}
	short, err2 := g.Run(ctx, "rev-parse", "--short", "HEAD")
	if err2 != nil {
		return "", err
	}
	return "HEAD (detached at " + strings.TrimSpace(short) + ")", nil
}

// Head — полный хеш HEAD; пусто без ошибки у репозитория без коммитов.
func (g Repo) Head(ctx context.Context) (string, error) {
	out, err := g.Run(ctx, "rev-parse", "--verify", "-q", "HEAD")
	if err != nil {
		if strings.Contains(err.Error(), "Needed a single revision") || strings.TrimSpace(out) == "" {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Tree — хеш дерева HEAD; с суффиксом «-dirty», если рабочее дерево
// отличается от него (изменённые или новые неигнорируемые файлы).
func (g Repo) Tree(ctx context.Context) (string, error) {
	head, err := g.Head(ctx)
	if err != nil {
		return "", err
	}
	if head == "" {
		return "", nil
	}
	out, err := g.Run(ctx, "rev-parse", head+"^{tree}")
	if err != nil {
		return "", err
	}
	tree := strings.TrimSpace(out)
	st, err := g.Status(ctx)
	if err != nil {
		return "", err
	}
	if len(st) > 0 {
		tree += "-dirty"
	}
	return tree, nil
}

// Status — строки `git status --porcelain` (пусто — дерево чистое).
func (g Repo) Status(ctx context.Context) ([]string, error) {
	out, err := g.Run(ctx, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

// Log — последние n коммитов (1 ≤ n ≤ MaxLog; вне диапазона — ближний
// предел). Репозиторий без коммитов — пустой список.
func (g Repo) Log(ctx context.Context, n int) ([]Commit, error) {
	if n <= 0 {
		n = 10
	}
	n = min(n, MaxLog)
	if head, err := g.Head(ctx); err != nil || head == "" {
		return nil, err
	}
	const sep = "\x1f"
	out, err := g.Run(ctx, "log", "-n", strconv.Itoa(n), "--format=%H"+sep+"%h"+sep+"%an"+sep+"%aI"+sep+"%s")
	if err != nil {
		return nil, err
	}
	var cs []Commit
	for _, l := range lines(out) {
		f := strings.SplitN(l, sep, 5)
		if len(f) < 5 {
			continue
		}
		cs = append(cs, Commit{Hash: f[0], Short: f[1], Author: f[2], Date: f[3], Subject: f[4]})
	}
	return cs, nil
}

// Diff — `git diff <rng>`; rng пустой — все незакоммиченные изменения
// относительно HEAD (у репозитория без коммитов — относительно пустого
// индекса). Текст длиннее max байт обрезается по границе строки, второе
// значение — был ли обрезан; max ≤ 0 — без предела.
func (g Repo) Diff(ctx context.Context, rng string, max int) (string, bool, error) {
	args := []string{"diff", "--no-color"}
	rng = strings.TrimSpace(rng)
	switch {
	case rng != "":
		if strings.HasPrefix(rng, "-") {
			return "", false, fmt.Errorf("диапазон %q похож на флаг", rng)
		}
		args = append(args, rng)
	default:
		if head, err := g.Head(ctx); err != nil {
			return "", false, err
		} else if head != "" {
			args = append(args, "HEAD")
		}
	}
	args = append(args, "--")
	out, err := g.Run(ctx, args...)
	if err != nil {
		return "", false, err
	}
	if max > 0 && len(out) > max {
		cut := out[:max]
		if i := strings.LastIndexByte(cut, '\n'); i > 0 {
			cut = cut[:i+1]
		}
		return cut, true, nil
	}
	return out, false, nil
}

// ChangedFiles — файлы, изменённые в диапазоне (`git diff --name-only`);
// rng пустой — изменённые относительно HEAD плюс новые неотслеживаемые.
func (g Repo) ChangedFiles(ctx context.Context, rng string) ([]string, error) {
	rng = strings.TrimSpace(rng)
	if rng != "" {
		if strings.HasPrefix(rng, "-") {
			return nil, fmt.Errorf("диапазон %q похож на флаг", rng)
		}
		out, err := g.Run(ctx, "diff", "--name-only", rng, "--")
		if err != nil {
			return nil, err
		}
		return lines(out), nil
	}
	args := []string{"diff", "--name-only"}
	if head, err := g.Head(ctx); err != nil {
		return nil, err
	} else if head != "" {
		args = append(args, "HEAD")
	}
	out, err := g.Run(ctx, append(args, "--")...)
	if err != nil {
		return nil, err
	}
	files := lines(out)
	untracked, err := g.Run(ctx, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return nil, err
	}
	files = append(files, lines(untracked)...)
	return files, nil
}

// ErrNoGit — git не найден в PATH.
var ErrNoGit = errors.New("git не найден в PATH")

// Available — есть ли git.
func Available() error {
	if _, err := exec.LookPath("git"); err != nil {
		return ErrNoGit
	}
	return nil
}

func lines(s string) []string {
	var out []string
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
