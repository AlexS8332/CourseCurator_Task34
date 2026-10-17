package review

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"aitr/34-files-agent/internal/gitx"
)

// DefaultMaxBytes — предел текста diff по умолчанию у FromRange (≈ 50 тыс.
// токенов): PR длиннее ревьюер читает по файлам через read_file.
const DefaultMaxBytes = 200 * 1024

// FromRange — вход ревью из диапазона git: `git diff <rng>` (обычно
// `main...HEAD` — изменения ветки от общего предка), разобранный по
// файлам; Title — тема последнего коммита диапазона, Base и Head — левая
// и правая части диапазона (пустая правая — HEAD; пустой rng —
// незакоммиченные изменения относительно HEAD). Diff длиннее maxBytes
// обрезается по границе строки с пометкой Truncated; файлы, не попавшие
// в обрезанный текст, остаются в Files без hunk'ов (из `--name-only`),
// чтобы ревью знало о них. maxBytes ≤ 0 — DefaultMaxBytes.
func FromRange(ctx context.Context, repo gitx.Repo, rng string, maxBytes int) (Input, error) {
	if maxBytes <= 0 {
		maxBytes = DefaultMaxBytes
	}
	rng = strings.TrimSpace(rng)
	if strings.HasPrefix(rng, "-") {
		return Input{}, fmt.Errorf("диапазон %q похож на флаг", rng)
	}
	diff, truncated, err := repo.Diff(ctx, rng, maxBytes)
	if err != nil {
		return Input{}, err
	}
	in := Input{Range: rng, Diff: diff, Truncated: truncated}
	in.Base, in.Head = splitRange(rng)
	in.Files, err = ParseUnified(diff)
	if err != nil {
		return Input{}, fmt.Errorf("diff %s: %w", rng, err)
	}
	if truncated {
		names, err := repo.ChangedFiles(ctx, rng)
		if err == nil {
			for _, n := range names {
				if FileByPath(in.Files, n) == nil {
					in.Files = append(in.Files, FileDiff{Path: NormPath(n), Status: StatusModified, Hunks: []Hunk{}})
				}
			}
		}
	}
	in.Title = rangeTitle(ctx, repo, in.Head, rng)
	return in, nil
}

// splitRange делит «base...head» / «base..head» на части; у одиночной
// ревизии base — она, head — HEAD; у пустого rng — HEAD без base.
func splitRange(rng string) (base, head string) {
	switch {
	case rng == "":
		return "", "HEAD"
	case strings.Contains(rng, "..."):
		i := strings.Index(rng, "...")
		base, head = rng[:i], rng[i+3:]
	case strings.Contains(rng, ".."):
		i := strings.Index(rng, "..")
		base, head = rng[:i], rng[i+2:]
	default:
		base = rng
	}
	if head == "" {
		head = "HEAD"
	}
	return base, head
}

// rangeTitle — тема последнего коммита head; если git её не даёт
// (незакоммиченные изменения, пустой репозиторий) — сам диапазон.
func rangeTitle(ctx context.Context, repo gitx.Repo, head, rng string) string {
	if rng != "" {
		if out, err := repo.Run(ctx, "log", "-1", "--format=%s", head, "--"); err == nil {
			if s := strings.TrimSpace(firstLine(out)); s != "" {
				return s
			}
		}
		return rng
	}
	return "незакоммиченные изменения"
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// FromPatch — вход ревью из файла патча (unified diff): подставные PR
// стенда и стенд окна. Title пустой — имя файла без расширения.
func FromPatch(path string, title string) (Input, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Input{}, err
	}
	text := strings.TrimPrefix(string(raw), "\uFEFF")
	files, err := ParseUnified(text)
	if err != nil {
		return Input{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if len(files) == 0 {
		return Input{}, fmt.Errorf("%s: в патче нет файлов", filepath.Base(path))
	}
	if strings.TrimSpace(title) == "" {
		title = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	}
	return Input{Title: title, Files: files, Diff: text}, nil
}

// FromPR — вход ревью из GitHub: заголовок, описание, ветки и sha головы
// из PR, diff — запросом с Accept application/vnd.github.diff. Head —
// sha головы (по нему маркер дедупа), Base — базовая ветка.
func FromPR(ctx context.Context, gh GitHub, owner, repo string, n int) (Input, error) {
	if gh == nil {
		return Input{}, errors.New("клиент GitHub не задан")
	}
	if n <= 0 {
		return Input{}, fmt.Errorf("номер PR %d", n)
	}
	pr, err := gh.PR(ctx, owner, repo, n)
	if err != nil {
		return Input{}, err
	}
	diff, err := gh.Diff(ctx, owner, repo, n)
	if err != nil {
		return Input{}, err
	}
	files, err := ParseUnified(diff)
	if err != nil {
		return Input{}, fmt.Errorf("PR #%d: %w", n, err)
	}
	head := pr.HeadSHA
	if head == "" {
		head = pr.Head
	}
	return Input{
		Title: pr.Title, Body: pr.Body, Base: pr.Base, Head: head, Number: n,
		Range: pr.Base + "..." + pr.Head, Files: files, Diff: diff,
	}, nil
}

// Paths — пути файлов входа по порядку.
func (in Input) Paths() []string {
	out := make([]string, 0, len(in.Files))
	for _, f := range in.Files {
		out = append(out, f.Path)
	}
	return out
}

// File — файл входа по пути; nil — нет.
func (in Input) File(p string) *FileDiff { return FileByPath(in.Files, p) }
