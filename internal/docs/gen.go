package docs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
)

// Имена сгенерированных документов (ID — «schema/<имя>»).
const (
	SchemaAssignments = "assignments.md"
	SchemaAitr        = "aitr.md"
)

// GenSchemas пишет в outDir два markdown-документа по реальным файлам
// корня и возвращает их как документы вида schema:
//
//   - assignments.md — поля assignment.json с примерами из настоящих
//     заданий, допустимые статусы и стеки (из aitr.ps1), устройство
//     docs/registry.json и таблица всех заданий;
//   - aitr.md — команды aitr (таблица из .claude/skills/aitr/SKILL.md и
//     диспетчер скрипта) и функции Invoke-Aitr* из scripts/aitr.ps1 с
//     первым комментарием каждой.
//
// Файлы пересоздаются при каждом вызове: это слепок, а не источник.
func GenSchemas(root *course.Root, outDir string) ([]corpus.Doc, error) {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, err
	}
	a, err := genAssignments(root)
	if err != nil {
		return nil, err
	}
	t, err := genAitr(root)
	if err != nil {
		return nil, err
	}
	var docs []corpus.Doc
	for _, f := range []struct{ name, text string }{{SchemaAssignments, a}, {SchemaAitr, t}} {
		if err := os.WriteFile(filepath.Join(outDir, f.name), []byte(f.text), 0o644); err != nil {
			return nil, err
		}
		d := ParseMarkdown(f.text)
		d.ID = "schema/" + f.name
		d.Source = string(Schema)
		d.URL = filepath.ToSlash(filepath.Join(outDir, f.name))
		docs = append(docs, d)
	}
	return docs, nil
}

// field — описание поля assignment.json.
type field struct{ name, typ, desc string }

var assignmentFields = []field{
	{"num", "string", "номер задания, две цифры с ведущим нулём; он же начало имени каталога"},
	{"slug", "string", "короткое имя в kebab-case; каталог задания — assignments/NN-slug"},
	{"title", "string", "название задания для INDEX.md и карточки"},
	{"stack", "string", "стек шаблона: go, python, node или prompt"},
	{"status", "string", "состояние: planned, in-progress, done или blocked; меняется командой aitr status"},
	{"created", "string", "дата создания, ГГГГ-ММ-ДД"},
	{"updated", "string", "дата последнего изменения метаданных, ГГГГ-ММ-ДД"},
	{"repo", "string", "адрес репозитория на GitHub; пусто — репозиторий не создан"},
	{"env", "string[]", "имена переменных окружения (ключей), которые нужны заданию; значения — в .env корня или .env.local задания"},
	{"tags", "string[]", "произвольные метки"},
}

func genAssignments(root *course.Root) (string, error) {
	all, err := root.Assignments()
	if err != nil {
		return "", err
	}
	statuses, stacks := psLists(root)
	var b strings.Builder
	b.WriteString("# Схема assignment.json и реестр заданий\n\n")
	b.WriteString("Файл `assignments/NN-slug/assignment.json` — единственный источник правды по заданию курса. ")
	b.WriteString("Его создаёт `aitr new`, статус меняет `aitr status`, а `aitr sync` собирает из всех таких файлов ")
	b.WriteString("таблицу `INDEX.md` и реестр `docs/registry.json`. Правят assignment.json — и запускают `aitr sync`; ")
	b.WriteString("править INDEX.md или registry.json руками бесполезно, их затрёт следующий sync.\n\n")

	example := pickExample(all)
	b.WriteString("## Поля assignment.json\n\n")
	b.WriteString("| Поле | Тип | Что значит | Пример |\n|---|---|---|---|\n")
	for _, f := range assignmentFields {
		fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` |\n", f.name, f.typ, f.desc, exampleValue(example, f.name))
	}
	if example.Num != "" {
		raw, _ := json.MarshalIndent(example, "", "  ")
		fmt.Fprintf(&b, "\nПример целиком — задание %s (`assignments/%s/assignment.json`):\n\n```json\n%s\n```\n", example.Num, example.DirName(), stripDir(string(raw)))
	}

	b.WriteString("\n## Статусы и стеки\n\n")
	fmt.Fprintf(&b, "Допустимые значения `status`: %s. ", code(statuses))
	b.WriteString("Новое задание получает planned; in-progress — идёт работа; done — сдано (карточка заполнена, контрольный прогон записан); blocked — ждёт внешнего условия.\n\n")
	fmt.Fprintf(&b, "Допустимые значения `stack`: %s. ", code(stacks))
	b.WriteString("Стек выбирает шаблон каталога (templates/<stack>) и проверки `aitr doctor`: go — go.mod и `go vet`/`go test`, python — `.venv` через uv и pytest, node — требует node (на машине курса его нет), prompt — без тулчейна.\n\n")

	b.WriteString("## Реестр docs/registry.json\n\n")
	b.WriteString("Генерируется `aitr sync`: объект `{generated, assignments[]}`, где `generated` — время сборки, а каждая запись — ")
	b.WriteString("`num`, `slug`, `title`, `stack`, `status`, `repo`, `dir` (`assignments/NN-slug`) и `card` (`docs/cards/NN-slug.md`). ")
	b.WriteString("Полей `env`, `tags`, `created`, `updated` в реестре нет. По реестру `aitr restore` клонирует задания на новой машине. ")
	b.WriteString("Расхождение assignment.json с реестром (drift) значит, что `aitr sync` не запускали после правки.\n\n")

	b.WriteString("## Задания курса\n\n")
	if len(all) == 0 {
		b.WriteString("Заданий пока нет.\n")
	} else {
		fmt.Fprintf(&b, "Всего заданий: %d. ", len(all))
		byStatus := map[string]int{}
		for _, a := range all {
			byStatus[a.Status]++
		}
		var parts []string
		for _, s := range statuses {
			if byStatus[s] > 0 {
				parts = append(parts, fmt.Sprintf("%s — %d", s, byStatus[s]))
			}
		}
		b.WriteString("По статусам: " + strings.Join(parts, ", ") + ".\n\n")
		b.WriteString("| № | Каталог | Название | Стек | Статус | Репозиторий |\n|---|---|---|---|---|---|\n")
		for _, a := range all {
			repo := a.Repo
			if repo == "" {
				repo = "—"
			}
			fmt.Fprintf(&b, "| %s | `assignments/%s` | %s | %s | %s | %s |\n", a.Num, a.DirName(), cell(a.Title), a.Stack, a.Status, repo)
		}
	}
	return b.String(), nil
}

// pickExample — задание для примера: последнее с заполненным repo и env,
// иначе последнее.
func pickExample(all []course.Assignment) course.Assignment {
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Repo != "" && len(all[i].Env) > 0 {
			return all[i]
		}
	}
	if len(all) > 0 {
		return all[len(all)-1]
	}
	return course.Assignment{}
}

func exampleValue(a course.Assignment, name string) string {
	switch name {
	case "num":
		return a.Num
	case "slug":
		return a.Slug
	case "title":
		return a.Title
	case "stack":
		return a.Stack
	case "status":
		return a.Status
	case "created":
		return a.Created
	case "updated":
		return a.Updated
	case "repo":
		return a.Repo
	case "env":
		return fmt.Sprintf("%q", a.Env)
	case "tags":
		return fmt.Sprintf("%q", a.Tags)
	}
	return ""
}

// stripDir убирает из JSON примера служебное поле dir: его в файле нет.
func stripDir(s string) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), `"dir":`) {
			continue
		}
		out = append(out, l)
	}
	// Запятая у последнего поля после удаления dir.
	for i := len(out) - 2; i >= 0; i-- {
		if strings.TrimSpace(out[i]) != "" {
			out[i] = strings.TrimSuffix(out[i], ",")
			break
		}
	}
	return strings.Join(out, "\n")
}

func code(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "`" + x + "`"
	}
	return strings.Join(q, ", ")
}

func cell(s string) string { return strings.ReplaceAll(strings.TrimSpace(s), "|", "\\|") }

var psListRe = regexp.MustCompile(`\$script:(AitrStatuses|AitrStacks)\s*=\s*@\(([^)]*)\)`)

// psLists — списки статусов и стеков из scripts/aitr.ps1; нет скрипта —
// умолчания course.
func psLists(root *course.Root) (statuses, stacks []string) {
	statuses, stacks = course.Statuses, course.Stacks
	raw, err := os.ReadFile(root.Path("scripts/aitr.ps1"))
	if err != nil {
		return
	}
	for _, m := range psListRe.FindAllStringSubmatch(string(raw), -1) {
		var vals []string
		for _, v := range strings.Split(m[2], ",") {
			v = strings.Trim(strings.TrimSpace(v), `'"`)
			if v != "" {
				vals = append(vals, v)
			}
		}
		if len(vals) == 0 {
			continue
		}
		if m[1] == "AitrStatuses" {
			statuses = vals
		} else {
			stacks = vals
		}
	}
	return
}

// psFunc — функция PowerShell с её описанием.
type psFunc struct {
	Name string
	Doc  string
	Line int
}

var (
	psFuncRe   = regexp.MustCompile(`^\s*function\s+([A-Za-z][\w-]*)\s*\{?`)
	psSwitchRe = regexp.MustCompile(`^\s*'([a-z-]+)'\s*\{\s*(Invoke-Aitr\w+)`)
)

// ParsePS — функции скрипта с первым комментарием: сплошной блок «#»
// прямо над `function`, а если его нет — первая строка-комментарий
// внутри тела (комментарии в шапке файла функциям не достаются).
func ParsePS(text string) []psFunc {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var out []psFunc
	for i, l := range lines {
		m := psFuncRe.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		doc := commentAbove(lines, i)
		if doc == "" {
			doc = commentInside(lines, i+1)
		}
		out = append(out, psFunc{Name: m[1], Doc: doc, Line: i + 1})
	}
	return out
}

func commentAbove(lines []string, i int) string {
	var block []string
	for j := i - 1; j >= 0; j-- {
		t := strings.TrimSpace(lines[j])
		if !strings.HasPrefix(t, "#") {
			break
		}
		t = strings.TrimSpace(strings.TrimLeft(t, "#"))
		if strings.HasPrefix(t, "---") {
			// Строка-разделитель «# ---- sync ----» — не описание.
			break
		}
		block = append([]string{t}, block...)
	}
	return strings.TrimSpace(strings.Join(block, " "))
}

func commentInside(lines []string, from int) string {
	depth := 1
	var block []string
	for j := from; j < len(lines) && depth > 0; j++ {
		t := strings.TrimSpace(lines[j])
		if strings.HasPrefix(t, "#") {
			block = append(block, strings.TrimSpace(strings.TrimLeft(t, "#")))
			continue
		}
		if len(block) > 0 {
			break
		}
		depth += strings.Count(t, "{") - strings.Count(t, "}")
		if psFuncRe.MatchString(t) {
			break
		}
	}
	return strings.TrimSpace(strings.Join(block, " "))
}

// skillTable — таблица «| Команда | Что делает |» из SKILL.md целиком.
func skillTable(text string) string {
	var out []string
	in := false
	for _, l := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		t := strings.TrimSpace(l)
		switch {
		case !in && strings.HasPrefix(t, "| Команда"):
			in = true
			out = append(out, t)
		case in && strings.HasPrefix(t, "|"):
			out = append(out, t)
		case in:
			return strings.Join(out, "\n")
		}
	}
	return strings.Join(out, "\n")
}

func genAitr(root *course.Root) (string, error) {
	var b strings.Builder
	b.WriteString("# Команды aitr и функции scripts/aitr.ps1\n\n")
	b.WriteString("`aitr` — инструментарий каталога курса: функции PowerShell 5.1 в `scripts/aitr.ps1`. ")
	b.WriteString("Скрипт подключают dot-source (`. scripts\\profile-snippet.ps1`), после чего в сессии есть функция `aitr`, ")
	b.WriteString("а она по первому слову вызывает `Invoke-Aitr<Команда>`. Всё, что меняет состав или метаданные заданий ")
	b.WriteString("(новое задание, статус, индекс и реестр), делается через aitr, а не руками.\n\n")

	b.WriteString("## Команды\n\n")
	if raw, err := os.ReadFile(root.Path(".claude/skills/aitr/SKILL.md")); err == nil {
		if t := skillTable(string(raw)); t != "" {
			b.WriteString(t + "\n\n")
		}
	}
	raw, err := os.ReadFile(root.Path("scripts/aitr.ps1"))
	if err != nil {
		b.WriteString("Скрипта scripts/aitr.ps1 нет.\n")
		return b.String(), nil
	}
	script := strings.ReplaceAll(string(raw), "\r\n", "\n")
	var cmds []string
	seen := map[string]bool{}
	for _, l := range strings.Split(script, "\n") {
		if m := psSwitchRe.FindStringSubmatch(l); m != nil && !seen[m[2]] {
			seen[m[2]] = true
			cmds = append(cmds, fmt.Sprintf("`aitr %s` → `%s`", m[1], m[2]))
		}
	}
	if len(cmds) > 0 {
		b.WriteString("Диспетчер `function aitr` в скрипте: " + strings.Join(cmds, "; ") + ".\n\n")
	}

	b.WriteString("## Функции scripts/aitr.ps1\n\n")
	b.WriteString("Команды пользователя — `Invoke-Aitr*`, остальное — помощники (пути, задания, файлы, окружение). Первый комментарий каждой функции:\n\n")
	funcs := ParsePS(script)
	sort.SliceStable(funcs, func(i, j int) bool {
		ii, jj := strings.HasPrefix(funcs[i].Name, "Invoke-"), strings.HasPrefix(funcs[j].Name, "Invoke-")
		if ii != jj {
			return ii
		}
		return funcs[i].Line < funcs[j].Line
	})
	for _, f := range funcs {
		doc := f.Doc
		if doc == "" {
			doc = "без комментария"
		}
		fmt.Fprintf(&b, "### %s\n\n%s (строка %d)\n\n", f.Name, doc, f.Line)
	}
	return b.String(), nil
}
