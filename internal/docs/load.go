// Package docs — документация курса как корпус базы знаний: markdown
// (карточки, конспекты, пособия, README, скиллы, шаблон карточки),
// условия заданий из week-tasks и сгенерированные описания схем
// (assignment.json, команды aitr) превращаются в corpus.Doc и
// индексируются стратегией structure, как статьи в v30.
//
// Что НЕ индексируется: INDEX.md и docs/registry.json (слепки, источник —
// assignment.json), docs/summ/result.json (сырой экспорт чата с личными
// данными), scripts/aitr.ps1 как текст (его описывают schema/aitr.md и
// документ вида code). Код — отдельный вид code (code.go): doc-комментарии
// и сигнатуры экспортов Go-пакетов, функции aitr.ps1.
package docs

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
)

// Kind — вид документа; он же Doc.Source и фильтр source у docs_search.
type Kind string

const (
	Card     Kind = "card"     // docs/cards/NN-slug.md
	Note     Kind = "note"     // docs/notes/*.md
	Handbook Kind = "handbook" // docs/summ/*.md (пособия, большие)
	Doc      Kind = "doc"      // остальные docs/**/*.md
	Readme   Kind = "readme"   // README.md корня, tools/*/README.md
	Skill    Kind = "skill"    // .claude/skills/*/SKILL.md
	Task     Kind = "task"     // week-tasks/*.txt, разделы по «День NN»
	Template Kind = "template" // templates/card.md
	Schema   Kind = "schema"   // сгенерировано GenSchemas
	// Code — объявлен в code.go: Go-пакеты и scripts/aitr.ps1.
)

// Kinds — все виды по порядку показа.
var Kinds = []Kind{Card, Note, Handbook, Doc, Readme, Skill, Task, Template, Schema, Code}

// skip — файлы, которые не индексируются ни под каким видом
// (относительные пути с «/»).
var skip = map[string]bool{
	"INDEX.md":              true,
	"docs/registry.json":    true,
	"docs/summ/result.json": true,
	"scripts/aitr.ps1":      true,
}

// KindOf — вид документа по относительному пути (с «/»); пусто — файл не
// индексируется.
func KindOf(rel string) Kind {
	rel = filepath.ToSlash(rel)
	if skip[rel] {
		return ""
	}
	base := filepath.Base(rel)
	dir := filepath.ToSlash(filepath.Dir(rel))
	switch {
	case rel == "README.md" || (strings.HasPrefix(rel, "tools/") && base == "README.md"):
		return Readme
	case strings.HasPrefix(rel, ".claude/skills/") && base == "SKILL.md":
		return Skill
	case strings.HasPrefix(rel, "week-tasks/") && strings.HasSuffix(base, ".txt"):
		return Task
	case rel == "templates/card.md":
		return Template
	case !strings.HasSuffix(base, ".md") || !strings.HasPrefix(rel, "docs/"):
		return ""
	case dir == "docs" && (strings.HasPrefix(base, "предложение-") || strings.HasPrefix(base, "решение-")):
		// Планы и решения по неделям описывают контрольные вопросы и
		// сценарии испытаний самого куратора — в индексе они подсказывали
		// бы ответы И-14. Это документы о разработке куратора, не о курсе.
		return ""
	case dir == "docs/cards" && base != "README.md":
		return Card
	case dir == "docs/notes" && base != "README.md":
		return Note
	case dir == "docs/summ":
		return Handbook
	default:
		return Doc
	}
}

// roots — где искать документы (каталоги обходятся рекурсивно, файлы —
// как есть).
var roots = []string{"README.md", "docs", "tools", ".claude/skills", "week-tasks", "templates/card.md"}

// Load читает все документы курса: markdown и week-tasks по roots плюс
// документы кода (CodeDocs). ID — относительный путь с «/», Source —
// Kind. Порядок — по ID.
func Load(root *course.Root) ([]corpus.Doc, error) {
	var docs []corpus.Doc
	for _, r := range roots {
		start := root.Path(r)
		st, err := os.Stat(start)
		if err != nil {
			continue
		}
		if !st.IsDir() {
			if d, ok, err := loadFile(root, r); err != nil {
				return nil, err
			} else if ok {
				docs = append(docs, d)
			}
			continue
		}
		err = filepath.WalkDir(start, func(p string, e os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if e.IsDir() {
				if n := e.Name(); n == ".git" || n == "node_modules" || n == "data" || n == "runs" || n == "testdata" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, _ := filepath.Rel(root.Dir, p)
			d, ok, err := loadFile(root, filepath.ToSlash(rel))
			if err != nil {
				return err
			}
			if ok {
				docs = append(docs, d)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	code, err := CodeDocs(root)
	if err != nil {
		return nil, err
	}
	docs = append(docs, code...)
	sort.Slice(docs, func(i, j int) bool { return docs[i].ID < docs[j].ID })
	return docs, nil
}

func loadFile(root *course.Root, rel string) (corpus.Doc, bool, error) {
	kind := KindOf(rel)
	if kind == "" {
		return corpus.Doc{}, false, nil
	}
	raw, err := os.ReadFile(root.Path(rel))
	if err != nil {
		return corpus.Doc{}, false, err
	}
	text := strings.TrimPrefix(string(raw), string(rune(0xFEFF)))
	var d corpus.Doc
	if kind == Task {
		d = ParseTasks(text)
	} else {
		d = ParseMarkdown(text)
	}
	d.ID = rel
	d.Source = string(kind)
	d.URL = rel
	if d.Title == "" {
		d.Title = strings.TrimSuffix(filepath.Base(rel), filepath.Ext(rel))
	}
	return d, true, nil
}

var (
	headingRe = regexp.MustCompile(`^(#{1,6})\s+(.*?)\s*#*\s*$`)
	dayRe     = regexp.MustCompile(`^\s*День\s+(\d+)\b`)
)

// ParseMarkdown — markdown → документ: Title из первого «#», Intro — текст
// до первого заголовка после него, Sections — по заголовкам «##…####»
// (Path — «Раздел › Подраздел», Level — число решёток). Повторный «#»
// (пособия с частями) открывает раздел первого уровня. Заголовки внутри
// огороженного кода (```) — текст, а не разделы. Текст раздела —
// собственный, без подразделов (так требует чанкер).
func ParseMarkdown(text string) corpus.Doc {
	type head struct {
		level int
		title string
	}
	var (
		d       corpus.Doc
		fence   bool
		body    strings.Builder
		stack   []head // открытые заголовки: путь текущего раздела
		started bool   // встретили заголовок после Title
	)
	flush := func() {
		t := strings.TrimSpace(body.String())
		body.Reset()
		if !started {
			d.Intro = t
			return
		}
		path := make([]string, len(stack))
		for i, h := range stack {
			path[i] = h.title
		}
		d.Sections = append(d.Sections, corpus.Section{
			Path: path, Title: path[len(path)-1], Level: stack[len(stack)-1].level, Text: t,
		})
	}
	sc := bufio.NewScanner(strings.NewReader(text))
	sc.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	for sc.Scan() {
		line := strings.TrimRight(sc.Text(), "\r")
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			body.WriteString(line + "\n")
			continue
		}
		m := headingRe.FindStringSubmatch(line)
		if fence || m == nil {
			body.WriteString(line + "\n")
			continue
		}
		lvl, title := len(m[1]), strings.TrimSpace(m[2])
		if lvl == 1 && d.Title == "" {
			// Первый «#» — заголовок документа; всё до него — тоже вступление.
			d.Title = title
			continue
		}
		if lvl > 4 {
			// «#####» и глубже — текст раздела, не свой раздел.
			body.WriteString(line + "\n")
			continue
		}
		flush()
		started = true
		// Путь — стек заголовков: закрываем все не ниже текущего уровня.
		for len(stack) > 0 && stack[len(stack)-1].level >= lvl {
			stack = stack[:len(stack)-1]
		}
		stack = append(stack, head{level: lvl, title: title})
	}
	flush()
	d.Schema = corpus.Schema
	if d.Sections == nil {
		d.Sections = []corpus.Section{}
	}
	return d
}

// ParseTasks — week-tasks/*.txt: один документ, разделы по строкам
// «День NN…» (заголовок раздела — вся строка), вступление — текст до
// первого дня. Title — «Задания курса: дни A–B» по найденным номерам.
func ParseTasks(text string) corpus.Doc {
	var (
		d    corpus.Doc
		body strings.Builder
		cur  string
		days []string
	)
	flush := func() {
		t := strings.TrimSpace(body.String())
		body.Reset()
		if cur == "" {
			d.Intro = t
			return
		}
		d.Sections = append(d.Sections, corpus.Section{Path: []string{cur}, Title: cur, Level: 2, Text: t})
	}
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		if m := dayRe.FindStringSubmatch(line); m != nil {
			flush()
			cur = strings.TrimSpace(strings.TrimRight(line, ".:"))
			days = append(days, m[1])
			continue
		}
		body.WriteString(line + "\n")
	}
	flush()
	d.Schema = corpus.Schema
	switch len(days) {
	case 0:
		d.Title = "Задания курса"
	case 1:
		d.Title = "Задания курса: день " + days[0]
	default:
		d.Title = fmt.Sprintf("Задания курса: дни %s–%s", days[0], days[len(days)-1])
	}
	if d.Sections == nil {
		d.Sections = []corpus.Section{}
	}
	return d
}

// Count — сколько документов каждого вида (для отчёта index).
func Count(docs []corpus.Doc) map[string]int {
	out := map[string]int{}
	for _, d := range docs {
		out[d.Source]++
	}
	return out
}
