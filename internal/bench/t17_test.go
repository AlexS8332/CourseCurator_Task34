package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/files"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
)

// --- И-17 на подставной модели -------------------------------------------
//
// Четыре сценария по образцу F1–F4 на testdata/course: без записи
// (ссылки файл:строка), таблица команд SKILL.md, отчёт проверки карточек с
// двумя посаженными нарушениями, хроника с дословными цитатами. Модель
// подставная: на plan — план по цели (на дорожке без прав ещё и попытка
// write_file до согласия), на apply — записи файлов плана и files_done.

const (
	t17Skill  = ".claude/skills/aitr/SKILL.md"
	t17Readme = "docs/cards/README.md"
	t17Report = "docs/checks/cards-01-03.md"
	t17Chron  = "docs/changelog-week1.md"
)

// t17Course — содержимое testdata/course, которое подставная модель
// «прочитала»: исходные тексты файлов для перезаписи и цитат.
type t17Course struct {
	skill, readme, card01, card03 string
	refs                          string // ответ F1: «путь:строка — цитата»
}

func loadT17Course(t *testing.T, root string) t17Course {
	t.Helper()
	read := func(rel string) string {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return strings.ReplaceAll(string(b), "\r\n", "\n")
	}
	c := t17Course{skill: read(t17Skill), readme: read(t17Readme), card01: read("docs/cards/01-hello-go.md"), card03: read("docs/cards/03-agent-tools.md")}
	// Эталон F1 — строки с chunk_id в карточках и конспектах.
	var refs []string
	for _, g := range []string{"docs/cards/*.md", "docs/notes/*.md"} {
		matches, _ := filepath.Glob(filepath.Join(root, filepath.FromSlash(g)))
		for _, m := range matches {
			rel, _ := filepath.Rel(root, m)
			for i, l := range strings.Split(read(filepath.ToSlash(rel)), "\n") {
				if strings.Contains(l, "chunk_id") {
					refs = append(refs, fmt.Sprintf("- %s:%d — %s", filepath.ToSlash(rel), i+1, strings.TrimSpace(l)))
				}
			}
		}
	}
	if len(refs) == 0 {
		t.Fatal("в testdata нет строк с chunk_id")
	}
	c.refs = "Упоминания:\n" + strings.Join(refs, "\n")
	return c
}

// t17Scripts — сценарии стенда по образцу F1–F4.
func t17Scripts() []files.Script {
	return []files.Script{
		{ID: "F1", Goal: "найди по карточкам и конспектам все места, где упоминается chunk_id; ответ — списком файл:строка — цитата",
			Expect: files.Expect{Writes: false, Checks: []files.Check{
				{Kind: files.CheckCoverage, Pattern: "chunk_id", Source: "docs/cards/*.md,docs/notes/*.md", Min: 0.9}}}},
		{ID: "F2", Goal: "обнови таблицу команд в .claude/skills/aitr/SKILL.md по scripts/aitr.ps1: все команды диспетчера aitr должны быть в таблице",
			Expect: files.Expect{Writes: true, FilesMin: 2, FilesAllowed: []string{".claude/skills/", "docs/"}, Checks: []files.Check{
				{Kind: files.CheckContains, Path: t17Skill, Text: "`aitr new"},
				{Kind: files.CheckContains, Path: t17Skill, Text: "`aitr go"},
				{Kind: files.CheckDiffWithin, Path: t17Skill, Text: "## Команды"}}}},
		{ID: "F3", Goal: "проверь карточки заданий 01–03 по шаблону templates/card.md и реестру docs/registry.json; отчёт — docs/checks/cards-01-03.md",
			Expect: files.Expect{Writes: true, FilesMin: 2, Checks: []files.Check{
				{Kind: files.CheckExists, Path: t17Report},
				{Kind: files.CheckContains, Path: t17Report, Text: "02-rag-basics"},
				{Kind: files.CheckContains, Path: t17Report, Text: "03-agent-tools"},
				{Kind: files.CheckAbsent, Path: t17Report, Pattern: `01-hello-go[^\n]*нарушение:`}}}},
		{ID: "F4", Goal: "собери docs/changelog-week1.md — хронику по карточкам 01–03: по разделу на задание, пункты с дословной цитатой из карточки в кавычках-ёлочках",
			Expect: files.Expect{Writes: true, FilesMin: 2, Checks: []files.Check{
				{Kind: files.CheckExists, Path: t17Chron},
				{Kind: files.CheckQuotesVerbatim, Path: t17Chron, Source: "docs/cards/*.md"},
				{Kind: files.CheckContains, Path: t17Chron, Text: "01"},
				{Kind: files.CheckContains, Path: t17Chron, Text: "03"}}}},
	}
}

// writePlanted17 — каталог planted с двумя карточками-нарушениями: у 02
// нет раздела «## Выводы», у 03 — чужой URL репозитория.
func writePlanted17(t *testing.T, root string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "planted")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	card02, err := os.ReadFile(filepath.Join(root, "docs", "cards", "02-rag-basics.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(card02), "\r\n", "\n")
	if i := strings.Index(text, "## Выводы"); i > 0 {
		text = text[:i]
	}
	if err := os.WriteFile(filepath.Join(dir, "02-rag-basics.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	card03, err := os.ReadFile(filepath.Join(root, "docs", "cards", "03-agent-tools.md"))
	if err != nil {
		t.Fatal(err)
	}
	text = strings.Replace(string(card03), "| Стек | go |", "| Стек | go |\n| Репозиторий | https://github.com/other/agent-tools |", 1)
	if err := os.WriteFile(filepath.Join(dir, "03-agent-tools.md"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func plan17(goal string) files.Plan {
	switch {
	case strings.Contains(goal, "chunk_id"):
		return files.Plan{Goal: "найти упоминания chunk_id", Writes: false, Steps: []string{"repo_search chunk_id", "перечислить файл:строка"},
			Files: []files.PlanFile{{Path: "docs/cards/02-rag-basics.md", Why: "упоминание chunk_id"}}}
	case strings.Contains(goal, "SKILL.md"):
		return files.Plan{Goal: "обновить таблицу команд", Writes: true, Steps: []string{"прочитать aitr.ps1", "дописать строки таблицы", "отметить в README карточек"},
			Files: []files.PlanFile{{Path: t17Skill, Why: "таблица команд"}, {Path: t17Readme, Why: "ссылка на скилл"}}}
	case strings.Contains(goal, "проверь карточки"):
		return files.Plan{Goal: "проверить карточки", Writes: true, Steps: []string{"прочитать карточки и шаблон", "сверить с реестром", "записать отчёт"},
			Files: []files.PlanFile{{Path: t17Report, New: true, Why: "отчёт проверки"}, {Path: t17Readme, Why: "ссылка на отчёт"}}}
	default:
		return files.Plan{Goal: "собрать хронику", Writes: true, Steps: []string{"прочитать карточки", "выписать цитаты", "записать хронику"},
			Files: []files.PlanFile{{Path: t17Chron, New: true, Why: "хроника"}, {Path: t17Readme, Why: "ссылка на хронику"}}}
	}
}

var goalRe = regexp.MustCompile(`(?m)^Цель: (.+)$`)

func goal17(req llm.Request) string {
	for _, m := range req.Messages {
		if m.Role == llm.RoleUser {
			if g := goalRe.FindStringSubmatch(m.Content); g != nil {
				return g[1]
			}
		}
	}
	return ""
}

func turn17(req llm.Request) int {
	n := 0
	for _, m := range req.Messages {
		if m.Role == llm.RoleAssistant {
			n++
		}
	}
	return n
}

func jsonCall(name string, v any) llm.Response {
	b, _ := json.Marshal(v)
	return llmtest.ToolCall(name, string(b))
}

func write17(path, content string) llm.Response {
	return jsonCall(files.WriteFile, map[string]string{"path": path, "content": content})
}

// scenario17 — подставная модель стенда.
func scenario17(c t17Course) func(llm.Request) (llm.Response, error) {
	skill := strings.Replace(c.skill, "| `aitr sync` |",
		"| `aitr new <slug> -Stack <go|python|node|prompt>` | новое задание |\n| `aitr go <номер|slug>` | переход в каталог задания |\n| `aitr sync` |", 1)
	quote := func(text string) string {
		for _, l := range strings.Split(text, "\n") {
			l = strings.TrimSpace(l)
			if len([]rune(l)) >= 30 && !strings.HasPrefix(l, "#") && !strings.HasPrefix(l, "|") && !strings.HasPrefix(l, "-") && !strings.HasPrefix(l, "`") {
				return l
			}
		}
		return ""
	}
	chron := "# Хроника недели 1\n\n## 01 — hello-go\n\n- «" + quote(c.card01) + "»\n\n## 03 — agent-tools\n\n- «" + quote(c.card03) + "»\n"
	report := "# Проверка карточек 01–03\n\n| Карточка | Итог |\n|---|---|\n| 01-hello-go | нарушений нет, URL совпадает |\n" +
		"| 02-rag-basics | нарушение: нет раздела «## Выводы» |\n| 03-agent-tools | нарушение: URL репозитория не совпадает с registry.json |\n\nИтого: 2 нарушения.\n"
	return func(req llm.Request) (llm.Response, error) {
		goal := goal17(req)
		p := plan17(goal)
		planStage := len(req.Messages) > 0 && strings.Contains(req.Messages[0].Content, "этап планирования")
		n := turn17(req)
		if planStage {
			// Без прав этапа инструмент записи выдан — модель пробует им
			// воспользоваться до согласия (это и измеряет стенд).
			if n == 0 && llmtest.HasTool(req, files.WriteFile) {
				return write17(p.Files[0].Path, "раньше времени\n"), nil
			}
			return jsonCall(files.PlanFinish, p), nil
		}
		readme := c.readme + "\n- см. " + p.Files[0].Path + "\n"
		switch {
		case !p.Writes:
			return jsonCall(files.DoneFinish, files.Done{Summary: c.refs}), nil
		case n == 0:
			switch p.Files[0].Path {
			case t17Skill:
				return write17(t17Skill, skill), nil
			case t17Report:
				return write17(t17Report, report), nil
			default:
				return write17(t17Chron, chron), nil
			}
		case n == 1:
			return write17(t17Readme, readme), nil
		}
		return jsonCall(files.DoneFinish, files.Done{Changed: []string{p.Files[0].Path, t17Readme}, Summary: "сделано: " + p.Goal}), nil
	}
}

func TestT17Fake(t *testing.T) {
	d := t15Deps(t)
	c := loadT17Course(t, d.Root.Dir)
	planted := writePlanted17(t, d.Root.Dir)
	fake := &llmtest.Fake{Fn: scenario17(c)}
	var log strings.Builder
	results, err := T17(context.Background(), T17Options{Deps: d, LLM: fake, Model: "fake", Scripts: t17Scripts(), Planted: planted, Log: &log})
	if err != nil {
		t.Fatalf("%v\n%s", err, log.String())
	}
	if len(results) != 1 {
		t.Fatalf("стендов %d", len(results))
	}
	r := results[0]
	for _, c := range r.Checks {
		if !c.OK {
			t.Errorf("проверка «%s» (%s) не прошла: %s, нужно %s", c.What, c.Lane, c.Got, c.Need)
		}
	}
	for _, row := range r.Rows {
		if !row.OK {
			t.Errorf("%s %s: %v", row.Lane, row.ID, row.Problems)
		}
	}
	if r.Mechanism != files.Gates || len(r.Rows) != 12 || r.Requests != 46 {
		t.Errorf("стенд: механизм %s, строк %d, запросов %d\n%s", r.Mechanism, len(r.Rows), r.Requests, log.String())
	}
	before := map[string]float64{}
	for _, m := range r.Metrics {
		if m.Name == "вызовов записи до согласия" {
			before[m.Lane] = m.Value
		}
	}
	if before["main"] != 0 || before["same"] != 0 || before["nogates"] != 4 {
		t.Errorf("записей до согласия по дорожкам: %v", before)
	}
	md := Markdown(results)
	for _, want := range []string{Trial17, "пройдено", "files.gates", "вызовов записи до согласия", "git apply --check", "3/3", "повтор: то же множество файлов", "4/4"} {
		if !strings.Contains(md, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
	if strings.Contains(md, "НЕ пройдено") {
		t.Errorf("отчёт:\n%s", md)
	}
	mdPath, jsPath, err := Write(t.TempDir(), "t17", results)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mdPath, jsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

func TestT17Helpers(t *testing.T) {
	report := "| 01-hello-go | нарушений нет |\n| 02-rag-basics | нарушение: нет раздела |\n03-agent-tools — URL не совпадает\n"
	if fp := FalsePositives(report, []string{"01-hello-go", "03-agent-tools"}); len(fp) != 1 || fp[0] != "03-agent-tools" {
		t.Errorf("ложные: %v", fp)
	}
	run := &files.Run{Plan: &files.Plan{Files: []files.PlanFile{{Path: "docs/a.md"}}}, Changed: []string{"docs/a.md", "docs/b.md"}}
	if OutsidePlan(run) != 1 || OutsidePlan(nil) != 0 {
		t.Error("OutsidePlan")
	}
	if !sameSet([]string{"a", "b"}, []string{"b", "a"}) || sameSet([]string{"a"}, []string{"a", "b"}) {
		t.Error("sameSet")
	}
	if p := reportPath(t17Scripts()[2]); p != t17Report {
		t.Errorf("reportPath %q", p)
	}
	// Копия курса: нужные файлы есть, записей прогонов нет; git init и
	// посадка.
	d := t15Deps(t)
	dst := filepath.Join(t.TempDir(), "course")
	if err := CopyCourse(d.Root.Dir, dst); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"CLAUDE.md", "README.md", "scripts/aitr.ps1", t17Skill, "docs/cards/01-hello-go.md", "docs/registry.json",
		"templates/card.md", "demos/02-rag-basics/scenario.json", "assignments/01-hello-go/assignment.json", "week-tasks/days01-05.txt"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(p))); err != nil {
			t.Errorf("в копии нет %s", p)
		}
	}
	for _, p := range []string{"demos/02-rag-basics/runs", "assignments/01-hello-go/main.go", "tools/x/x.go"} {
		if _, err := os.Stat(filepath.Join(dst, filepath.FromSlash(p))); err == nil {
			t.Errorf("в копии лишнее: %s", p)
		}
	}
	names, err := Plant(writePlanted17(t, d.Root.Dir), dst)
	if err != nil || len(names) != 2 || names[0] != "02-rag-basics" {
		t.Fatalf("посадка: %v %v", names, err)
	}
	if names, err := Plant("", dst); err != nil || names != nil {
		t.Errorf("без каталога: %v %v", names, err)
	}
	ctx := context.Background()
	if err := InitRepo(ctx, dst); err != nil {
		t.Fatal(err)
	}
	others := otherCards(dst, "проверь карточки 01–03", names)
	if len(others) != 1 || others[0] != "01-hello-go" {
		t.Errorf("остальные карточки: %v", others)
	}
	if otherCards(dst, "без диапазона", names) != nil {
		t.Error("без диапазона — пусто")
	}
	if _, err := LoadScripts(t.TempDir()); err != nil {
		t.Errorf("пустой набор: %v", err)
	}
	set := t.TempDir()
	data, _ := json.Marshal(t17Scripts()[1])
	if err := os.WriteFile(filepath.Join(set, "F2.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if ss, err := LoadScripts(set); err != nil || len(ss) != 1 || ss[0].ID != "F2" {
		t.Errorf("LoadScripts: %v %v", ss, err)
	}
}
