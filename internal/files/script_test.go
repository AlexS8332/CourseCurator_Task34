package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/gitx"
)

func writeT(t *testing.T, root, rel, text string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	os.MkdirAll(filepath.Dir(p), 0o755)
	if err := os.WriteFile(p, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadScript(t *testing.T) {
	dir := t.TempDir()
	good := `{"id":"T1","goal":"цель","expect":{"files_min":2,"files_allowed":["docs/"],"writes":true,
	  "checks":[{"kind":"exists","path":"./docs\\x.md"},{"kind":"contains","path":"docs/x.md","text":"a"},
	  {"kind":"regex","path":"docs/x.md","pattern":"^a"},{"kind":"diff_within","path":"docs/x.md","text":"## Р"},
	  {"kind":"quotes_verbatim","path":"docs/x.md","source":"docs/cards/*.md","min":0.9},
	  {"kind":"coverage","pattern":"x","source":"docs/*.md","min":0.9},{"kind":"absent","path":"docs/x.md","pattern":"z"}]}}`
	writeT(t, dir, "good.json", good)
	s, err := LoadScript(filepath.Join(dir, "good.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "T1" || s.Expect.FilesMin != 2 || !s.Expect.Writes || len(s.Expect.Checks) != 7 || s.Expect.Checks[0].Path != "docs/x.md" {
		t.Fatalf("сценарий: %+v", s)
	}
	if !s.Allows("docs/a.md") || s.Allows("README.md") || !(Script{}).Allows("README.md") {
		t.Fatal("Allows")
	}
	bad := map[string]string{
		"не json":       `{`,
		"нет id":        `{"goal":"x"}`,
		"нет goal":      `{"id":"x"}`,
		"kind":          `{"id":"x","goal":"x","expect":{"checks":[{"kind":"нет","path":"a"}]}}`,
		"contains без":  `{"id":"x","goal":"x","expect":{"checks":[{"kind":"contains","path":"a"}]}}`,
		"regex без":     `{"id":"x","goal":"x","expect":{"checks":[{"kind":"regex","path":"a","text":"t"}]}}`,
		"плохой regex":  `{"id":"x","goal":"x","expect":{"checks":[{"kind":"regex","path":"a","pattern":"("}]}}`,
		"exists без":    `{"id":"x","goal":"x","expect":{"checks":[{"kind":"exists"}]}}`,
		"diff_within":   `{"id":"x","goal":"x","expect":{"checks":[{"kind":"diff_within","path":"a"}]}}`,
		"quotes без":    `{"id":"x","goal":"x","expect":{"checks":[{"kind":"quotes_verbatim","path":"a"}]}}`,
		"coverage без":  `{"id":"x","goal":"x","expect":{"checks":[{"kind":"coverage","pattern":"a"}]}}`,
		"min > 1":       `{"id":"x","goal":"x","expect":{"checks":[{"kind":"exists","path":"a","min":2}]}}`,
		"range":         `{"id":"x","goal":"x","expect":{"checks":[{"kind":"exists","path":"a","range":[5,2]}]}}`,
		"путь наружу":   `{"id":"x","goal":"x","expect":{"checks":[{"kind":"exists","path":"../a"}]}}`,
		"files_allowed": `{"id":"x","goal":"x","expect":{"files_allowed":[""]}}`,
		"files_min < 0": `{"id":"x","goal":"x","expect":{"files_min":-1}}`,
	}
	for name, text := range bad {
		writeT(t, dir, "bad.json", text)
		if _, err := LoadScript(filepath.Join(dir, "bad.json")); err == nil {
			t.Errorf("%s: прошёл", name)
		}
	}
	if _, err := LoadScript(filepath.Join(dir, "none.json")); err == nil {
		t.Error("нет файла — должна быть ошибка")
	}
}

// Сценарии набора загружаются и описывают то, что обещает README.
func TestEvalScriptsLoad(t *testing.T) {
	dir := filepath.Join("..", "..", "eval", "files")
	want := map[string]bool{"F1": false, "F2": true, "F3": true, "F4": true}
	for id, writes := range want {
		s, err := LoadScript(filepath.Join(dir, id+".json"))
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if s.ID != id || s.Expect.Writes != writes || len(s.Expect.Checks) == 0 {
			t.Errorf("%s: id=%s writes=%v checks=%d", id, s.ID, s.Expect.Writes, len(s.Expect.Checks))
		}
		if writes && s.Expect.FilesMin < 2 {
			t.Errorf("%s: files_min %d < 2", id, s.Expect.FilesMin)
		}
	}
	// Посаженные карточки — те же имена, что в docs/cards, и в planted.json.
	for _, name := range []string{"27-animal-guide-local-app.md", "29-animal-guide-local-tune.md"} {
		if _, err := os.Stat(filepath.Join(dir, "planted", name)); err != nil {
			t.Errorf("planted/%s: %v", name, err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "planted", "planted.json"))
	if err != nil || !strings.Contains(string(raw), "27-animal-guide-local-app.md") || !strings.Contains(string(raw), "29-animal-guide-local-tune.md") {
		t.Errorf("planted.json: %v", err)
	}
	// Карточка 27 без «## Выводы», у 29 — чужой URL.
	c27, _ := os.ReadFile(filepath.Join(dir, "planted", "27-animal-guide-local-app.md"))
	if strings.Contains(string(c27), "## Выводы") || !strings.Contains(string(c27), "## Грабли") {
		t.Error("planted/27: раздел «## Выводы» должен отсутствовать, остальные — на месте")
	}
	c29, _ := os.ReadFile(filepath.Join(dir, "planted", "29-animal-guide-local-tune.md"))
	if !strings.Contains(string(c29), "| Репозиторий | `https://github.com/") || !strings.Contains(string(c29), "## Выводы") {
		t.Error("planted/29: в таблице должен быть URL репозитория, разделы — на месте")
	}
}

func TestEvaluateExistsContainsAbsentRegex(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, "docs/x.md", "# Заголовок\n\nпервая строка\nвторая: Ёж и ёж\n")
	cases := []struct {
		c   Check
		ok  bool
		got string
	}{
		{Check{Kind: CheckExists, Path: "docs/x.md"}, true, "байт"},
		{Check{Kind: CheckExists, Path: "docs/none.md"}, false, "нет"},
		{Check{Kind: CheckExists, Path: "docs"}, false, "каталог"},
		{Check{Kind: CheckContains, Path: "docs/x.md", Text: "вторая: Ёж"}, true, "строка 4"},
		{Check{Kind: CheckContains, Path: "docs/x.md", Text: "третья"}, false, "нет"},
		{Check{Kind: CheckContains, Path: "docs/none.md", Text: "x"}, false, "нет"},
		{Check{Kind: CheckContains, Path: "docs/x.md", Pattern: `(?m)^# \S+`}, true, "строка 1"},
		{Check{Kind: CheckRegex, Path: "docs/x.md", Pattern: `(?i)ёж и ёж`}, true, "строка 4"},
		{Check{Kind: CheckRegex, Path: "docs/x.md", Pattern: `^вторая`}, false, "нет"},
		{Check{Kind: CheckAbsent, Path: "docs/x.md", Text: "третья"}, true, "нет"},
		{Check{Kind: CheckAbsent, Path: "docs/x.md", Text: "первая"}, false, "не должно"},
		{Check{Kind: CheckAbsent, Path: "docs/x.md", Pattern: `(?m)^.*вторая.*Ёж.*$`}, false, "не должно"},
		{Check{Kind: CheckAbsent, Path: "docs/none.md", Text: "x"}, false, "нет"},
		{Check{Kind: "нет такого", Path: "docs/x.md"}, false, "kind"},
	}
	for _, tc := range cases {
		r := Evaluate(root, tc.c)
		if r.OK != tc.ok || !strings.Contains(r.Got, tc.got) {
			t.Errorf("%+v: ok=%v got=%q", tc.c, r.OK, r.Got)
		}
	}
}

func TestEvaluateQuotesVerbatim(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, "docs/cards/26-a.md", "# 26\n\nМалой модели нужна классификация вместо\n  оценки — «что делает фрагмент» она решает верно.\n")
	writeT(t, root, "docs/cards/27-b.md", "# 27\n\n**Переключить всё приложение на локальную модель — одна строка;** заставить его работать — пять механизмов.\n")
	writeT(t, root, "docs/cards/other.txt", "не источник\n")
	src := "docs/cards/2*.md"
	// Дословно: перенос строки и ё/е, кавычки внутри цитаты — нормализуются.
	writeT(t, root, "docs/ch.md", "- 26: «малой модели нужна классификация вместо оценки - “что делает фрагмент” она решает верно»\n"+
		"- 27: «заставить его работать — пять механизмов»\n- «коротко» — не цитата\n")
	r := Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/ch.md", Source: src})
	if !r.OK || !strings.Contains(r.Got, "2 из 2") {
		t.Fatalf("дословные: %v %s", r.OK, r.Got)
	}
	// Одна выдуманная из трёх: 67 % — ниже 1.0, но выше 0.6.
	writeT(t, root, "docs/ch.md", "«малой модели нужна классификация вместо оценки» и «заставить его работать — пять механизмов», а ещё «этой фразы в карточках никогда не было, она выдумана»\n")
	r = Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/ch.md", Source: src})
	if r.OK || !strings.Contains(r.Got, "2 из 3") || !strings.Contains(r.Got, "не найдено: «этой фразы") {
		t.Fatalf("выдуманная: %v %s", r.OK, r.Got)
	}
	if r := Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/ch.md", Source: src, Min: 0.6}); !r.OK {
		t.Fatalf("порог 0.6: %s", r.Got)
	}
	// Свой минимум длины: короткая цитата становится проверяемой.
	writeT(t, root, "docs/ch.md", "«коротко и мимо»\n")
	if r := Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/ch.md", Source: src}); r.OK || !strings.Contains(r.Got, "нет цитат") {
		t.Fatalf("без цитат: %v %s", r.OK, r.Got)
	}
	if r := Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/ch.md", Source: src, Range: [2]int{5, 0}}); r.OK || !strings.Contains(r.Got, "0 из 1") {
		t.Fatalf("свой порог длины: %v %s", r.OK, r.Got)
	}
	if r := Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/ch.md", Source: "docs/none/*.md", Range: [2]int{5, 0}}); r.OK || !strings.Contains(r.Got, "файлов нет") {
		t.Fatalf("нет источников: %v %s", r.OK, r.Got)
	}
	if r := Evaluate(root, Check{Kind: CheckQuotesVerbatim, Path: "docs/none.md", Source: src}); r.OK {
		t.Fatal("нет файла")
	}
}

func TestEvaluateCoverage(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, "docs/cards/24-x.md", "финишёр kb_answer\n")
	writeT(t, root, "docs/cards/25-y.md", "ничего\n")
	writeT(t, root, "docs/notes/n1.md", "help_answer тут\n")
	writeT(t, root, "docs/notes/n2.md", "и review_done\n")
	c := Check{Kind: CheckCoverage, Pattern: "kb_answer|help_answer|review_done", Source: "docs/cards/*.md, docs/notes/*.md", Min: 0.9}
	c.Text = "docs/cards/24-x.md:1 — финишёр\nn1.md:1 — help_answer\n`docs/notes/n2`:1 — review_done"
	r := Evaluate(root, c)
	if !r.OK || !strings.Contains(r.Got, "3 из 3") {
		t.Fatalf("полнота: %v %s", r.OK, r.Got)
	}
	c.Text = "docs/cards/24-x.md:1 — финишёр, а 25-y.md ни при чём"
	r = Evaluate(root, c)
	if r.OK || !strings.Contains(r.Got, "1 из 3") || !strings.Contains(r.Got, "не упомянуты: docs/notes/n1.md, docs/notes/n2.md") {
		t.Fatalf("неполно: %v %s", r.OK, r.Got)
	}
	c.Min = 0.3
	if r := Evaluate(root, c); !r.OK {
		t.Fatalf("порог 0.3: %s", r.Got)
	}
	// Текст из файла.
	writeT(t, root, "docs/answer.md", "n1.md, n2.md, 24-x.md\n")
	if r := Evaluate(root, Check{Kind: CheckCoverage, Pattern: "kb_answer|help_answer|review_done", Source: "docs/**", Path: "docs/answer.md"}); r.OK {
		t.Fatalf("** не раскрывается filepath.Glob — эталон пуст: %v %s", r.OK, r.Got)
	}
	if r := Evaluate(root, Check{Kind: CheckCoverage, Pattern: "kb_answer|help_answer|review_done", Source: "docs/cards/*.md,docs/notes/*.md", Path: "docs/answer.md"}); !r.OK {
		t.Fatalf("из файла: %s", r.Got)
	}
	if r := Evaluate(root, Check{Kind: CheckCoverage, Pattern: "нет_такого", Source: "docs/cards/*.md", Text: "x"}); r.OK || !strings.Contains(r.Got, "эталон пуст") {
		t.Fatalf("пустой эталон: %v %s", r.OK, r.Got)
	}
	// Checks подставляет summary в coverage без text/path.
	s := Script{ID: "x", Goal: "x"}
	s.Expect.Checks = []Check{{Kind: CheckCoverage, Pattern: "kb_answer|help_answer|review_done", Source: "docs/cards/*.md,docs/notes/*.md"},
		{Kind: CheckExists, Path: "docs/answer.md"}}
	rs := Checks(root, s, "24-x.md, n1.md, n2.md")
	if len(rs) != 2 || !AllOK(rs) || rs[0].Check.Text != "24-x.md, n1.md, n2.md" {
		t.Fatalf("Checks: %+v", rs)
	}
	if AllOK(Checks(root, s, "ничего")) {
		t.Fatal("пустой summary не должен проходить")
	}
}

func TestCitations(t *testing.T) {
	root := t.TempDir()
	writeT(t, root, "docs/cards/24-x.md", "# 24\n\nфинишёр `kb_answer` проверяется кодом\nещё строка\n")
	text := "Нашёл:\n- docs/cards/24-x.md:3 — «финишёр kb_answer проверяется кодом»\n" +
		"2. `docs/cards/24-x.md`:4 - ещё строка\n" +
		"- docs/cards/24-x.md:9 — за концом файла\n" +
		"- docs/cards/none.md:1 — нет файла\n" +
		"- docs/cards/24-x.md:4 — не та цитата\n" +
		"просто текст без ссылки\n"
	total, ok, bad := Citations(root, text)
	if total != 5 || ok != 2 || len(bad) != 3 {
		t.Fatalf("total=%d ok=%d bad=%v", total, ok, bad)
	}
	if !strings.Contains(bad[0], "в файле 4 строк") || !strings.Contains(bad[1], "файла нет") || !strings.Contains(bad[2], "не содержит") {
		t.Fatalf("объяснения: %v", bad)
	}
	if total, _, _ := Citations(root, "без ссылок"); total != 0 {
		t.Fatal("ссылок нет")
	}
}

func TestSectionRange(t *testing.T) {
	text := "# Т\n\nвводная\n\n## Команды\n\n| a |\n| b |\n\n### Под\n\nx\n\n## Секреты\n\ny\n"
	if from, to := sectionRange(text, "## Команды"); from != 5 || to != 13 {
		t.Fatalf("раздел: %d–%d", from, to)
	}
	if from, to := sectionRange(text, "## Секреты"); from != 14 || to != 16 {
		t.Fatalf("последний раздел: %d–%d", from, to)
	}
	if from, to := sectionRange(text, "### Под"); from != 10 || to != 13 {
		t.Fatalf("подраздел: %d–%d", from, to)
	}
	if from, _ := sectionRange(text, "## Нет"); from != 0 {
		t.Fatal("нет раздела")
	}
	if from, _ := sectionRange(text, "Команды"); from != 0 {
		t.Fatal("не заголовок")
	}
}

func TestEvaluateDiffWithin(t *testing.T) {
	root := testRoot(t)
	ctx := ctxT(t)
	wt, err := gitx.AddWorktree(ctx, root, filepath.Join(root.Dir, "sandbox", "agent", "d1"), "agent/d1")
	if err != nil {
		t.Fatal(err)
	}
	c := Check{Kind: CheckDiffWithin, Path: "docs/a.md", Text: "## Раздел"}
	if r := Evaluate(wt.Dir, c); r.OK || !strings.Contains(r.Got, "не изменён") {
		t.Fatalf("без изменений: %v %s", r.OK, r.Got)
	}
	// Правка внутри раздела (грязное дерево → diff к HEAD).
	writeT(t, wt.Dir, "docs/a.md", "# A\n\n## Раздел\n\nодин\nдва-два\nтри\nчетыре\n\n## Другой\n\nконец\n")
	if r := Evaluate(wt.Dir, c); !r.OK || !strings.Contains(r.Got, "3 изменённых") {
		t.Fatalf("внутри: %v %s", r.OK, r.Got)
	}
	// Правка снаружи: строка «конец» в «## Другой».
	writeT(t, wt.Dir, "docs/a.md", "# A\n\n## Раздел\n\nодин\nдва-два\nтри\n\n## Другой\n\nфинал\n")
	r := Evaluate(wt.Dir, c)
	if r.OK || !strings.Contains(r.Got, "вне раздела") || !strings.Contains(r.Got, "-11") || !strings.Contains(r.Got, "+11") {
		t.Fatalf("снаружи: %v %s", r.OK, r.Got)
	}
	// Явный диапазон строк.
	if r := Evaluate(wt.Dir, Check{Kind: CheckDiffWithin, Path: "docs/a.md", Range: [2]int{5, 12}}); !r.OK {
		t.Fatalf("range: %s", r.Got)
	}
	if r := Evaluate(wt.Dir, Check{Kind: CheckDiffWithin, Path: "docs/a.md", Range: [2]int{5, 8}}); r.OK {
		t.Fatalf("range узкий: %s", r.Got)
	}
	if r := Evaluate(wt.Dir, Check{Kind: CheckDiffWithin, Path: "docs/a.md", Text: "## Нет"}); r.OK || !strings.Contains(r.Got, "нет раздела") {
		t.Fatalf("нет раздела: %v %s", r.OK, r.Got)
	}
	// Новый файл: раздела в базе не было — всё добавленное внутри нового.
	writeT(t, wt.Dir, "docs/n.md", "# N\n\n## Раздел\n\nтекст\n")
	if r := Evaluate(wt.Dir, Check{Kind: CheckDiffWithin, Path: "docs/n.md", Text: "## Раздел"}); r.OK {
		t.Fatalf("строка «# N» вне раздела: %s", r.Got)
	}
	if r := Evaluate(wt.Dir, Check{Kind: CheckDiffWithin, Path: "docs/n.md", Text: "# N"}); !r.OK {
		t.Fatalf("весь новый файл под «# N»: %s", r.Got)
	}
	// После коммита дерево чистое → diff HEAD~1..HEAD.
	writeT(t, wt.Dir, "docs/a.md", "# A\n\n## Раздел\n\nодин\nдва-два\nтри\n\n## Другой\n\nконец\n")
	os.Remove(filepath.Join(wt.Dir, "docs", "n.md"))
	if err := wt.AddAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.Commit(ctx, "правка раздела"); err != nil {
		t.Fatal(err)
	}
	if r := Evaluate(wt.Dir, c); !r.OK || !strings.Contains(r.Got, "2 изменённых") {
		t.Fatalf("после коммита: %v %s", r.OK, r.Got)
	}
	if r := Evaluate(t.TempDir(), c); r.OK || !strings.Contains(r.Got, "git") {
		t.Fatalf("не репозиторий: %v %s", r.OK, r.Got)
	}
}

func TestVerify(t *testing.T) {
	s := Script{ID: "x", Goal: "x"}
	s.Expect.FilesMin = 2
	s.Expect.FilesAllowed = []string{"docs/", ".claude/skills/"}
	s.Expect.Writes = true
	r := &Run{Changed: []string{"docs/a.md", ".claude/skills/aitr/SKILL.md"}, Plan: &Plan{Writes: true}}
	rs := Verify(r, s)
	if len(rs) != 3 || !AllOK(rs) {
		t.Fatalf("ожидания: %+v", rs)
	}
	r = &Run{Changed: []string{"README.md"}, Plan: &Plan{Writes: false}}
	rs = Verify(r, s)
	if AllOK(rs) || rs[0].OK || rs[1].OK || rs[2].OK {
		t.Fatalf("всё мимо: %+v", rs)
	}
	if !strings.Contains(rs[0].Got, "затронуто 1") || !strings.Contains(rs[1].Got, "README.md") || !strings.Contains(rs[2].Got, "writes=false") {
		t.Fatalf("объяснения: %+v", rs)
	}
	if rs := Verify(nil, Script{}); len(rs) != 3 || !rs[0].OK || !rs[1].OK || rs[2].OK {
		t.Fatalf("пустой прогон: %+v", rs)
	}
}
