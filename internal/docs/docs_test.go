package docs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/db"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/kb"
)

func testRoot(t *testing.T) *course.Root {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "testdata", "course"))
	if err != nil {
		t.Fatal(err)
	}
	return &course.Root{Dir: dir}
}

func byID(docs []corpus.Doc, id string) corpus.Doc {
	for _, d := range docs {
		if d.ID == id {
			return d
		}
	}
	return corpus.Doc{}
}

func TestKindOf(t *testing.T) {
	cases := map[string]Kind{
		"README.md": Readme, "tools/edgerec/README.md": Readme, ".claude/skills/aitr/SKILL.md": Skill,
		"week-tasks/days31-35.txt": Task, "templates/card.md": Template,
		"docs/cards/01-hello-go.md": Card, "docs/cards/README.md": Doc, "docs/notes/02-rag.md": Note,
		"docs/summ/handbook.md": Handbook, "docs/course.md": Doc, "docs/snippets/x.md": Doc,
		"INDEX.md": "", "docs/registry.json": "", "docs/summ/result.json": "", "scripts/aitr.ps1": "",
		"CLAUDE.md": "", "assignments/01-x/README.md": "", "tools/curator/CHANGELOG.md": "",
	}
	for rel, want := range cases {
		if got := KindOf(rel); got != want {
			t.Errorf("%s: %q, ждал %q", rel, got, want)
		}
	}
}

func TestLoad(t *testing.T) {
	docs, err := Load(testRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	n := Count(docs)
	want := map[string]int{"card": 3, "note": 1, "handbook": 1, "doc": 1, "readme": 2, "skill": 1, "task": 1, "template": 1, "code": 2}
	for k, v := range want {
		if n[k] != v {
			t.Errorf("%s: %d, ждал %d", k, n[k], v)
		}
	}
	if len(docs) != 13 {
		t.Errorf("документов %d: %v", len(docs), n)
	}
	for _, d := range docs {
		if strings.Contains(d.ID, "\\") || d.Source == "" || d.Title == "" || d.Schema != corpus.Schema {
			t.Errorf("документ %+v", d)
		}
	}

	card := byID(docs, "docs/cards/01-hello-go.md")
	if card.Title != "01 — Первый запрос к LLM (Go)" {
		t.Errorf("заголовок карточки: %q", card.Title)
	}
	if !strings.Contains(card.Intro, "| Стек | go |") {
		t.Errorf("вступление карточки — таблица: %q", card.Intro)
	}
	var paths []string
	for _, s := range card.Sections {
		paths = append(paths, strings.Join(s.Path, " › "))
	}
	if got := strings.Join(paths, "|"); got != "Цель|Что сделал|Что сделал › Структура|Грабли|Выводы" {
		t.Errorf("разделы: %s", got)
	}
	if s := card.Sections[1]; s.Level != 2 || !strings.Contains(s.Text, "Таймаут 60 секунд") || strings.Contains(s.Text, "main.go") {
		t.Errorf("собственный текст раздела «Что сделал»: %+v", s)
	}
	if s := card.Sections[2]; s.Level != 3 || !strings.Contains(s.Text, "main.go") {
		t.Errorf("подраздел: %+v", s)
	}

	// Заголовок внутри ``` — текст, не раздел.
	c3 := byID(docs, "docs/cards/03-agent-tools.md")
	for _, s := range c3.Sections {
		if s.Title == "не заголовок" {
			t.Error("заголовок из блока кода стал разделом")
		}
	}

	// Повторный «#» в пособии — раздел первого уровня, под ним «##» и «###».
	hb := byID(docs, "docs/summ/handbook.md")
	paths = nil
	for _, s := range hb.Sections {
		paths = append(paths, strings.Join(s.Path, " › "))
	}
	if got := strings.Join(paths, "|"); got != "Содержание|Часть I. Основы|Часть I. Основы › 1. Модель как функция|Часть I. Основы › 1. Модель как функция › 1.1. У модели нет состояния|Часть I. Основы › 1. Модель как функция › 1.2. Токены|Часть I. Основы › 2. Контекст-инжиниринг" {
		t.Errorf("пособие: %s", got)
	}

	// Условия — по дням.
	task := byID(docs, "week-tasks/days01-05.txt")
	if task.Title != "Задания курса: дни 1–3" || !strings.Contains(task.Intro, "Неделя 1") {
		t.Errorf("условия: %q %q", task.Title, task.Intro)
	}
	if len(task.Sections) != 3 || task.Sections[1].Title != "День 2. Формат ответа" || !strings.Contains(task.Sections[2].Text, "git-ветки") {
		t.Errorf("дни: %+v", task.Sections)
	}
}

func TestParseMarkdownEdge(t *testing.T) {
	d := ParseMarkdown("текст без заголовков\n")
	if d.Title != "" || d.Intro != "текст без заголовков" || len(d.Sections) != 0 {
		t.Fatalf("%+v", d)
	}
	d = ParseMarkdown("# T\n\n## A\n\n#### Глубокий\n\nтело\n\n## B\n")
	if len(d.Sections) != 3 || d.Sections[1].Level != 4 || strings.Join(d.Sections[1].Path, "/") != "A/Глубокий" || d.Sections[2].Title != "B" {
		t.Fatalf("%+v", d.Sections)
	}
	text := d.Text()
	if !strings.Contains(text, "## A › Глубокий\nтело") {
		t.Fatalf("канонический текст: %q", text)
	}
}

func TestGenSchemas(t *testing.T) {
	out := t.TempDir()
	docs, err := GenSchemas(testRoot(t), out)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].ID != "schema/assignments.md" || docs[1].ID != "schema/aitr.md" || docs[0].Source != "schema" {
		t.Fatalf("%+v", docs)
	}
	a, _ := os.ReadFile(filepath.Join(out, SchemaAssignments))
	for _, want := range []string{"| `status` |", "`planned`, `in-progress`, `done`, `blocked`", "| 02 | `assignments/02-rag-basics` |",
		"Всего заданий: 2", `"env": [`, "DEEPSEEK_API_KEY", "## Реестр docs/registry.json"} {
		if !strings.Contains(string(a), want) {
			t.Errorf("assignments.md без %q", want)
		}
	}
	if strings.Contains(string(a), `"dir":`) {
		t.Error("в примере не должно быть служебного dir")
	}
	s, _ := os.ReadFile(filepath.Join(out, SchemaAitr))
	for _, want := range []string{"| `aitr doctor <номер\\|slug>` |", "`aitr sync` → `Invoke-AitrSync`", "### Invoke-AitrDoctor",
		"Проверка среды задания: метаданные, git, тулчейн, ключи, документация.", "### Get-AitrAssignments", "Задания по assignments/*/assignment.json."} {
		if !strings.Contains(string(s), want) {
			t.Errorf("aitr.md без %q", want)
		}
	}
	if strings.Contains(string(s), "утилиты ---") {
		t.Error("строка-разделитель попала в описание")
	}
	// Invoke-* идут первыми.
	if strings.Index(string(s), "### Invoke-AitrSync") > strings.Index(string(s), "### Get-AitrPaths") {
		t.Error("Invoke-* должны быть раньше помощников")
	}
}

func TestParsePS(t *testing.T) {
	fs := ParsePS("# шапка\n\nfunction A {\n    # внутри A\n    x\n}\n# над B\n# вторая строка\nfunction B { }\nfunction C { param($x) }\n")
	if len(fs) != 3 || fs[0].Doc != "внутри A" || fs[1].Doc != "над B вторая строка" || fs[2].Doc != "" {
		t.Fatalf("%+v", fs)
	}
}

func openStore(t *testing.T) *kb.Store {
	t.Helper()
	s, err := kb.Open(context.Background(), db.Memory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestIndexAndStale(t *testing.T) {
	ctx := context.Background()
	root := testRoot(t)
	loaded, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := GenSchemas(root, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	all := append(loaded, schemas...)
	store := openStore(t)

	// Без индекса — устарел.
	if stale, why, err := Stale(ctx, store, root); err != nil || !stale || !strings.Contains(why, "не собран") {
		t.Fatalf("без индекса: %v %q %v", stale, why, err)
	}

	info, err := Index(ctx, store, all, nil, "abc123", "tree456-dirty")
	if err != nil {
		t.Fatal(err)
	}
	if info.ID != IndexID || info.Params.Max != MaxRune || info.Params.Min != MinRune || info.Chunks == 0 || info.Embedder != "" {
		t.Fatalf("индекс: %+v", info)
	}
	meta, err := ReadMeta(ctx, store)
	if err != nil || meta.Commit != "abc123" || meta.Tree != "tree456-dirty" || meta.Docs != len(all) || meta.BuiltAt.IsZero() {
		t.Fatalf("мета: %+v %v", meta, err)
	}
	docsInDB, _ := store.Docs(ctx)
	if len(docsInDB) != len(all) {
		t.Fatalf("в базе %d документов, ждал %d (служебная запись манифеста не документ)", len(docsInDB), len(all))
	}
	chunks, _ := store.Chunks(ctx, IndexID, "")
	kinds := map[string]bool{}
	for _, c := range chunks {
		kinds[c.Source] = true
		if c.Source == "card" && c.Title == "" {
			t.Errorf("чанк без заголовка: %+v", c)
		}
	}
	for _, k := range []string{"card", "note", "handbook", "schema", "task", "skill", "readme"} {
		if !kinds[k] {
			t.Errorf("нет чанков вида %s", k)
		}
	}

	stale, why, err := Stale(ctx, store, root)
	if err != nil || stale {
		t.Fatalf("сразу после сборки: %v %q %v", stale, why, err)
	}

	// Изменённый документ → устарел, с числом изменённых.
	changed := make([]corpus.Doc, len(all))
	copy(changed, all)
	for i := range changed {
		if changed[i].Source == "card" {
			changed[i].Intro += "\nправка"
			break
		}
	}
	if _, err := Index(ctx, store, changed, nil, "def", "tree"); err != nil {
		t.Fatal(err)
	}
	stale, why, err = Stale(ctx, store, root)
	if err != nil || !stale || !strings.Contains(why, "изменено 1") {
		t.Fatalf("после правки: %v %q %v", stale, why, err)
	}

	// Поиск BM25 по индексу находит карточку.
	s := &kb.Searcher{Store: store}
	hits, sinfo, err := s.Search(ctx, "права этапа", kb.SearchOptions{Index: IndexID, K: 3})
	if err != nil || len(hits) == 0 || sinfo.Mode != kb.BM25 {
		t.Fatalf("поиск: %v %+v %v", hits, sinfo, err)
	}
	if hits[0].DocID != "docs/cards/03-agent-tools.md" {
		t.Errorf("первый — %s, ждал карточку 03", hits[0].DocID)
	}
}

func TestIndexWithEmbedder(t *testing.T) {
	ctx := context.Background()
	docs, _ := Load(testRoot(t))
	store := openStore(t)
	info, err := Index(ctx, store, docs, embed.Hash{D: 16}, "c", "t")
	if err != nil {
		t.Fatal(err)
	}
	if info.Embedder != "hash-16" || info.Dims != 16 {
		t.Fatalf("%+v", info)
	}
}

func TestState(t *testing.T) {
	// testdata лежит внутри репозитория корня курса, но его верх — не
	// testdata: коммит и дерево должны остаться пустыми.
	c := State(context.Background(), testRoot(t), nil)
	if c.Commit != "" || c.Tree != "" || c.DocsSHA == "" || c.Docs != 13 {
		t.Fatalf("%+v", c)
	}
}
