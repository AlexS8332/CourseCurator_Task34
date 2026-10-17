// Package course — каталог курса как объект: корень, задания
// (assignments/*/assignment.json), реестр docs/registry.json и
// расхождения между ними, последний контрольный прогон задания
// (demos/NN-slug/runs/<время>/report.json).
//
// Пакет повторяет логику scripts/aitr.ps1 (Get-AitrAssignments,
// Resolve-AitrAssignment, Invoke-AitrSync), но ничего не пишет: источник
// правды по заданию — assignment.json, реестр и INDEX.md пересобирает
// только `aitr sync`.
package course

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Root — корень каталога курса: там лежат CLAUDE.md и scripts/aitr.ps1.
type Root struct{ Dir string }

// Assignment — задание. Поля — assignment.json как есть; Dir — абсолютный
// путь каталога задания (у реестра — вычисляется из поля dir).
type Assignment struct {
	Num     string   `json:"num"`
	Slug    string   `json:"slug"`
	Title   string   `json:"title"`
	Stack   string   `json:"stack"`
	Status  string   `json:"status"`
	Repo    string   `json:"repo"`
	Created string   `json:"created,omitempty"`
	Updated string   `json:"updated,omitempty"`
	Env     []string `json:"env,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Dir     string   `json:"dir"`
}

// DirName — имя каталога задания: «NN-slug».
func (a Assignment) DirName() string { return filepath.Base(a.Dir) }

// Статусы и стеки, которые знает aitr (см. $script:AitrStatuses и
// $script:AitrStacks в scripts/aitr.ps1).
var (
	Statuses = []string{"planned", "in-progress", "done", "blocked"}
	Stacks   = []string{"go", "python", "node", "prompt"}
)

// ErrNotRoot — ни в dir, ни выше по дереву нет каталога курса.
var ErrNotRoot = errors.New("каталог курса не найден: нужны CLAUDE.md и scripts/aitr.ps1")

// IsRoot — похож ли каталог на корень курса.
func IsRoot(dir string) bool {
	for _, p := range []string{"CLAUDE.md", filepath.Join("scripts", "aitr.ps1")} {
		if st, err := os.Stat(filepath.Join(dir, p)); err != nil || st.IsDir() {
			return false
		}
	}
	return true
}

// Open ищет корень курса в dir и выше по дереву (как git ищет .git).
// Пустой dir — текущий каталог.
func Open(dir string) (*Root, error) {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	for d := abs; ; {
		if IsRoot(d) {
			return &Root{Dir: d}, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return nil, fmt.Errorf("%w (искал от %s)", ErrNotRoot, abs)
		}
		d = parent
	}
}

// Path — абсолютный путь внутри корня по относительному (с любыми
// разделителями).
func (r *Root) Path(rel string) string {
	return filepath.Join(r.Dir, filepath.FromSlash(rel))
}

// Assignments — задания по assignments/*/assignment.json в порядке имён
// каталогов, как Get-AitrAssignments. Каталог без assignment.json
// пропускается; битый JSON — ошибка с путём (aitr лишь предупреждает, но
// ассистенту лучше знать точно).
func (r *Root) Assignments() ([]Assignment, error) {
	dir := filepath.Join(r.Dir, "assignments")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Assignment
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		meta := filepath.Join(dir, e.Name(), "assignment.json")
		raw, err := os.ReadFile(meta)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var a Assignment
		if err := json.Unmarshal(stripBOM(raw), &a); err != nil {
			return nil, fmt.Errorf("битый assignment.json: %s: %w", meta, err)
		}
		a.Dir = filepath.Join(dir, e.Name())
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DirName() < out[j].DirName() })
	return out, nil
}

// registryFile — docs/registry.json, как его пишет Invoke-AitrSync.
type registryFile struct {
	Generated   string          `json:"generated"`
	Assignments []registryEntry `json:"assignments"`
}

type registryEntry struct {
	Num    string `json:"num"`
	Slug   string `json:"slug"`
	Title  string `json:"title"`
	Stack  string `json:"stack"`
	Status string `json:"status"`
	Repo   string `json:"repo"`
	Dir    string `json:"dir"`
	Card   string `json:"card"`
}

// Registry — задания по docs/registry.json (без env, tags и дат: реестр
// их не хранит). Нет реестра — пустой список без ошибки.
func (r *Root) Registry() ([]Assignment, error) {
	raw, err := os.ReadFile(filepath.Join(r.Dir, "docs", "registry.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f registryFile
	if err := json.Unmarshal(stripBOM(raw), &f); err != nil {
		return nil, fmt.Errorf("docs/registry.json: %w", err)
	}
	out := make([]Assignment, 0, len(f.Assignments))
	for _, e := range f.Assignments {
		dir := e.Dir
		if dir == "" {
			dir = "assignments/" + e.Num + "-" + e.Slug
		}
		out = append(out, Assignment{Num: e.Num, Slug: e.Slug, Title: e.Title, Stack: e.Stack,
			Status: e.Status, Repo: e.Repo, Dir: r.Path(dir)})
	}
	return out, nil
}

// Drift — расхождения assignment.json ↔ docs/registry.json: задание без
// записи в реестре, запись без задания, разные title/stack/status/repo.
// Пусто — реестр свежий. Каждая строка — готовая фраза для ответа.
func (r *Root) Drift() ([]string, error) {
	live, err := r.Assignments()
	if err != nil {
		return nil, err
	}
	reg, err := r.Registry()
	if err != nil {
		return nil, err
	}
	byDir := map[string]Assignment{}
	for _, a := range reg {
		byDir[a.DirName()] = a
	}
	var out []string
	seen := map[string]bool{}
	for _, a := range live {
		seen[a.DirName()] = true
		b, ok := byDir[a.DirName()]
		if !ok {
			out = append(out, fmt.Sprintf("%s: нет в docs/registry.json (нужен aitr sync)", a.DirName()))
			continue
		}
		for _, f := range []struct{ name, live, reg string }{
			{"title", a.Title, b.Title}, {"stack", a.Stack, b.Stack},
			{"status", a.Status, b.Status}, {"repo", a.Repo, b.Repo},
		} {
			if f.live != f.reg {
				out = append(out, fmt.Sprintf("%s: %s в assignment.json %q, в реестре %q", a.DirName(), f.name, f.live, f.reg))
			}
		}
	}
	for _, b := range reg {
		if !seen[b.DirName()] {
			out = append(out, fmt.Sprintf("%s: есть в реестре, но каталога задания нет", b.DirName()))
		}
	}
	return out, nil
}

// ErrNoAssignment — задания с таким ключом нет.
var ErrNoAssignment = errors.New("задание не найдено")

// Assignment ищет задание по ключу, как Resolve-AitrAssignment: номер
// («05»), slug, имя каталога — точно; иначе однозначная часть имени
// каталога. Номер без ведущего нуля («5») тоже принимается.
func (r *Root) Assignment(key string) (Assignment, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Assignment{}, errors.New("не указано задание (номер или slug)")
	}
	all, err := r.Assignments()
	if err != nil {
		return Assignment{}, err
	}
	if len(all) == 0 {
		return Assignment{}, fmt.Errorf("%w: заданий пока нет", ErrNoAssignment)
	}
	num := key
	if len(key) == 1 && key[0] >= '0' && key[0] <= '9' {
		num = "0" + key
	}
	var exact, fuzzy []Assignment
	for _, a := range all {
		switch {
		case a.Num == key || a.Num == num || a.Slug == key || a.DirName() == key:
			exact = append(exact, a)
		case strings.Contains(a.DirName(), key):
			fuzzy = append(fuzzy, a)
		}
	}
	for _, set := range [][]Assignment{exact, fuzzy} {
		switch len(set) {
		case 1:
			return set[0], nil
		case 0:
			continue
		default:
			names := make([]string, len(set))
			for i, a := range set {
				names[i] = a.DirName()
			}
			return Assignment{}, fmt.Errorf("неоднозначно: %s", strings.Join(names, ", "))
		}
	}
	return Assignment{}, fmt.Errorf("%w: %s", ErrNoAssignment, key)
}

// CardPath — путь карточки задания (docs/cards/NN-slug.md), существует она
// или нет.
func (r *Root) CardPath(a Assignment) string {
	return filepath.Join(r.Dir, "docs", "cards", a.DirName()+".md")
}

// DemoDir — каталог контрольного прогона задания (demos/NN-slug).
func (r *Root) DemoDir(a Assignment) string {
	return filepath.Join(r.Dir, "demos", a.DirName())
}

// stripBOM убирает UTF-8 BOM: PowerShell 5.1 иногда пишет файлы с ним, а
// encoding/json его не терпит.
func stripBOM(b []byte) []byte {
	return []byte(strings.TrimPrefix(string(b), string(rune(0xFEFF))))
}
