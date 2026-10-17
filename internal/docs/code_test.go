package docs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
)

func sectionByPath(d corpus.Doc, path string) (corpus.Section, bool) {
	for _, s := range d.Sections {
		if strings.Join(s.Path, " › ") == path {
			return s, true
		}
	}
	return corpus.Section{}, false
}

func TestCodeDocs(t *testing.T) {
	docs, err := CodeDocs(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID)
		if d.Source != string(Code) || d.Schema != corpus.Schema || d.Title == "" || d.URL != d.ID {
			t.Errorf("документ: %+v", d)
		}
	}
	// tools/edgerec в testdata — только README, Go-файлов нет: пакета нет.
	if got := strings.Join(ids, ","); got != "scripts/aitr.ps1,tools/x" {
		t.Fatalf("документы кода: %s", got)
	}

	x := byID(docs, "tools/x")
	if x.Title != "пакет x (tools/x)" {
		t.Errorf("заголовок: %q", x.Title)
	}
	if !strings.HasPrefix(x.Intro, "Package x — маленький пакет") || !strings.Contains(x.Intro, "Файлы: x.go.") {
		t.Errorf("вступление: %q", x.Intro)
	}
	if strings.Contains(x.Intro, "Второй абзац") == false {
		// doc-комментарий пакета берётся целиком — это его описание.
		t.Errorf("вступление без второго абзаца: %q", x.Intro)
	}
	var paths []string
	for _, s := range x.Sections {
		paths = append(paths, strings.Join(s.Path, " › "))
	}
	if got := strings.Join(paths, "|"); got != "Типы|Типы › Thing|Функции|Функции › Version" {
		t.Errorf("разделы: %s", got)
	}
	th, _ := sectionByPath(x, "Типы › Thing")
	if th.Level != 3 || !strings.Contains(th.Text, "type Thing struct {") || !strings.Contains(th.Text, "Name string") {
		t.Errorf("тип: %q", th.Text)
	}
	if !strings.Contains(th.Text, "Thing — вещь с именем.") || strings.Contains(th.Text, "Подробности") {
		t.Errorf("первый абзац типа: %q", th.Text)
	}
	// Конструктор New go/doc относит к типу: он в разделе Thing вместе с методом.
	if !strings.Contains(th.Text, "func New(name string) (*Thing, error) — New — новая вещь с именем name.") || !strings.Contains(th.Text, "func (t *Thing) Count() int — Count возвращает счётчик.") {
		t.Errorf("конструктор и метод в разделе типа: %q", th.Text)
	}
	if strings.Contains(th.Text, "errors.New") {
		t.Errorf("тело функции в индекс не идёт: %q", th.Text)
	}
	ver, _ := sectionByPath(x, "Функции › Version")
	if !strings.Contains(ver.Text, "func Version() string") || !strings.Contains(ver.Text, "Version — версия пакета. Вторая строка того же абзаца.") || strings.Contains(ver.Text, "Третий абзац") || strings.Contains(ver.Text, `return "1"`) {
		t.Errorf("функция: %q", ver.Text)
	}
	if strings.Contains(x.Text(), "hidden") || strings.Contains(x.Text(), "Tests") {
		t.Error("неэкспортированное или тестовое попало в индекс")
	}

	ps := byID(docs, "scripts/aitr.ps1")
	if ps.Title != "скрипт scripts/aitr.ps1" || !strings.Contains(ps.Intro, "Заглушка scripts/aitr.ps1") || !strings.Contains(ps.Intro, "PowerShell 5.1: без `&&`") {
		t.Errorf("aitr.ps1: title=%q intro=%q", ps.Title, ps.Intro)
	}
	doctor, ok := sectionByPath(ps, "Функции › Invoke-AitrDoctor")
	if !ok || doctor.Level != 3 || !strings.Contains(doctor.Text, "function Invoke-AitrDoctor {") || !strings.Contains(doctor.Text, "param([string[]]$Tokens)") {
		t.Errorf("функция doctor: %+v", doctor)
	}
	if !strings.Contains(doctor.Text, "Проверка среды задания") || !strings.Contains(doctor.Text, "строка 30") {
		t.Errorf("комментарий и строка: %q", doctor.Text)
	}
	assign, _ := sectionByPath(ps, "Функции › Get-AitrAssignments")
	if !strings.Contains(assign.Text, "Задания по assignments/*/assignment.json") {
		t.Errorf("комментарий внутри тела: %q", assign.Text)
	}
	if _, ok := sectionByPath(ps, "Функции › aitr"); !ok {
		t.Error("диспетчер aitr — тоже функция")
	}
}

func TestLoadIncludesCode(t *testing.T) {
	docs, err := Load(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	if Count(docs)["code"] != 2 {
		t.Errorf("code в Load: %v", Count(docs))
	}
	if KindOf("scripts/aitr.ps1") != "" || KindOf("tools/x/x.go") != "" {
		t.Error("код не идёт через KindOf")
	}
	found := false
	for _, k := range Kinds {
		if k == Code {
			found = true
		}
	}
	if !found {
		t.Error("Code нет в Kinds")
	}
}

func TestFirstParagraph(t *testing.T) {
	if got := firstParagraph("Первый\nабзац.\n\nВторой."); got != "Первый абзац." {
		t.Errorf("%q", got)
	}
	if got := firstSentence("Первое предложение. Второе."); got != "Первое предложение." {
		t.Errorf("%q", got)
	}
	if got := firstSentence("v1.2 без точки в конце"); got != "v1.2 без точки в конце" {
		t.Errorf("%q", got)
	}
}

// TestCodeDocsCourseRoot — на настоящем корне курса (CURATOR_COURSE_ROOT
// или поиск вверх; иначе Skip) все Go-пакеты edgerec и куратора
// разбираются, у каждого есть вступление и разделы, aitr.ps1 — с
// функциями Invoke-Aitr*.
func TestCodeDocsCourseRoot(t *testing.T) {
	dir := strings.TrimSpace(os.Getenv("CURATOR_COURSE_ROOT"))
	if dir == "" {
		wd, _ := os.Getwd()
		for d := wd; ; d = filepath.Dir(d) {
			if _, err := os.Stat(filepath.Join(d, "CLAUDE.md")); err == nil {
				if _, err := os.Stat(filepath.Join(d, "scripts", "aitr.ps1")); err == nil {
					dir = d
					break
				}
			}
			if filepath.Dir(d) == d {
				break
			}
		}
	}
	if dir == "" {
		t.Skip("корень курса не найден")
	}
	docs, err := CodeDocs(&course.Root{Dir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]corpus.Doc{}
	for _, d := range docs {
		ids[d.ID] = d
		if d.Intro == "" || len(d.Sections) == 0 {
			t.Errorf("%s: без вступления или разделов", d.ID)
		}
	}
	for _, want := range []string{"tools/edgerec", "tools/curator/internal/gitx", "tools/curator/internal/docs", "tools/curator/cmd/curator", "scripts/aitr.ps1"} {
		if _, ok := ids[want]; !ok {
			t.Errorf("нет документа %s (есть %d)", want, len(docs))
		}
	}
	if g := ids["tools/curator/internal/gitx"]; !strings.Contains(g.Text(), "func (g Repo) Diff(ctx context.Context, rng string, max int) (string, bool, error)") {
		t.Errorf("gitx: сигнатуры методов Repo: %q", clipText(g.Text(), 400))
	}
	if ps := ids["scripts/aitr.ps1"]; !strings.Contains(ps.Text(), "## Функции › Invoke-AitrRecord") {
		t.Error("aitr.ps1: нет Invoke-AitrRecord")
	}
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
