package bench

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/llm/llmtest"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/review"
)

// --- И-15 на подставной модели -------------------------------------------

const (
	ruleShell15  = "Windows, PowerShell 5.1 (без &&, ||, тернарников и ??). Доступны: git, gh, go, uv."
	rulePush15   = "Коммиты создавай сам, а git push (и тем более force-push) — только с явного разрешения пользователя. Значения ключей не печатай."
	ruleExpect15 = "expectJS — выражение, а не присваивание: результат должен быть истинным; долгие ожидания (wait больше 30 секунд) сопровождай cut."
)

func t15Deps(t *testing.T) project.Deps {
	t.Helper()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "course.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	docs := []corpus.Doc{
		{Schema: 1, ID: "CLAUDE.md", Source: "doc", Title: "Правила работы в каталоге курса", Intro: "Рабочий каталог курса.",
			Sections: []corpus.Section{
				{Path: []string{"Инструменты"}, Title: "Инструменты", Level: 2, Text: ruleShell15},
				{Path: []string{"Коммиты и публикация"}, Title: "Коммиты и публикация", Level: 2, Text: rulePush15},
			}},
		{Schema: 1, ID: "tools/edgerec/README.md", Source: "readme", Title: "edgerec", Intro: "Контрольный прогон в Edge.",
			Sections: []corpus.Section{{Path: []string{"Сценарий"}, Title: "Сценарий", Level: 2, Text: ruleExpect15}}},
	}
	if err := st.PutCorpus(ctx, docs, corpus.Manifest{Schema: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	root, err := course.Open(filepath.Join("..", "..", "testdata", "course"))
	if err != nil {
		t.Fatal(err)
	}
	return project.Deps{Root: root, Store: st, Searcher: &kb.Searcher{Store: st, Embedder: embed.Hash{}}, Index: string(kb.Structure)}
}

// Патчи подставных PR теста (unified diff, применять не нужно).
var t15Patches = map[string]string{
	"P1": `diff --git a/tools/edgerec/report.go b/tools/edgerec/report.go
--- a/tools/edgerec/report.go
+++ b/tools/edgerec/report.go
@@ -40,6 +40,7 @@ func writeReport(dir string, r Report) error {
 	data, err := json.MarshalIndent(r, "", "  ")
 	if err != nil {
 		return err
 	}
-	return os.WriteFile(filepath.Join(dir, "report.json"), data, 0o644)
+	_ = os.WriteFile(filepath.Join(dir, "report.json"), data, 0o644)
+	return nil
 }
`,
	"P2": `diff --git a/scripts/aitr.ps1 b/scripts/aitr.ps1
--- a/scripts/aitr.ps1
+++ b/scripts/aitr.ps1
@@ -12,4 +12,5 @@ function Get-AitrPaths {
 function Get-AitrPaths {
     $root = $script:AitrRoot
+    git fetch && git pull
     return [pscustomobject]@{ Root = $root }
 }
`,
	"P3": `diff --git a/demos/02-rag-basics/scenario.json b/demos/02-rag-basics/scenario.json
--- a/demos/02-rag-basics/scenario.json
+++ b/demos/02-rag-basics/scenario.json
@@ -5,4 +5,6 @@
   "steps": [
     {"wait": 1000},
+    {"expectJS": "x = 1; true"},
+    {"wait": 240000},
     {"shot": "main"}
   ]
`,
	"P4": `diff --git a/scripts/publish.ps1 b/scripts/publish.ps1
new file mode 100644
--- /dev/null
+++ b/scripts/publish.ps1
@@ -0,0 +1,4 @@
+param([string]$Branch = 'main')
+Write-Host "key: $env:DEEPSEEK_API_KEY"
+git push --force origin $Branch
+Write-Host 'done'
`,
	"P5": `diff --git a/docs/notes/README.md b/docs/notes/README.md
--- a/docs/notes/README.md
+++ b/docs/notes/README.md
@@ -1,3 +1,3 @@
 # Конспекты

-Конспекты по темам курса лижат здесь.
+Конспекты по темам курса лежат здесь.
`,
}

// writePlanted — патчи во временный каталог и записи Planted с полными
// путями (как после review.LoadPlanted).
func writePlanted(t *testing.T) (string, []review.Planted) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "planted"), 0o755); err != nil {
		t.Fatal(err)
	}
	for id, text := range t15Patches {
		if err := os.WriteFile(filepath.Join(dir, "planted", id+".patch"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	patch := func(id string) string { return filepath.Join(dir, "planted", id+".patch") }
	prs := []review.Planted{
		{ID: "P1", Title: "edgerec: отчёт прогона", Patch: patch("P1"), File: "tools/edgerec/report.go", Line: 44, Kind: review.KindBug},
		{ID: "P2", Title: "aitr: обновление перед запуском", Patch: patch("P2"), File: "scripts/aitr.ps1", Line: 14, Kind: review.KindRule},
		{ID: "P3", Title: "сценарий 02: ожидание", Patch: patch("P3"), File: "demos/02-rag-basics/scenario.json", Line: 7, Kind: review.KindRule},
		{ID: "P4", Title: "скрипт публикации", Patch: patch("P4"), File: "scripts/publish.ps1", Line: 3, Kind: review.KindRule},
		{ID: "P5", Title: "конспекты: опечатка", Patch: patch("P5"), Clean: true},
	}
	return dir, prs
}

func finish15(r review.Review) llm.Response {
	b, _ := json.Marshal(r)
	return llmtest.ToolCall(review.FinishName, string(b))
}

// rule15 — находка rule с цитатой фрагмента h, если выдача есть; без
// выдачи (дорожка без базы) модель правила не знает и даёт recommendation.
func rule15(h cite.SearchHit, ok bool, file string, line int, text string) review.Finding {
	if !ok {
		return review.Finding{File: file, Line: line, Kind: review.KindRecommendation, Severity: review.SeverityMinor, Text: text}
	}
	quote := string([]rune(h.Text)[:min(40, len([]rune(h.Text)))])
	return review.Finding{File: file, Line: line, Kind: review.KindRule, Severity: review.SeverityMajor, Text: text,
		Evidence: review.Evidence{ChunkID: h.ChunkID, Quote: quote}}
}

// scenario15 — подставная модель: bug видит по diff на любой дорожке;
// правила цитирует по выдаче docs_search, а без неё — рекомендации;
// чистый PR — «замечаний нет».
func scenario15(req llm.Request) (llm.Response, error) {
	q := firstUser(req)
	changes := func(f review.Finding, summary string) (llm.Response, error) {
		return finish15(review.Review{Findings: []review.Finding{f}, Summary: summary, Verdict: review.VerdictChanges}), nil
	}
	switch {
	case strings.Contains(q, "report.go"):
		return changes(review.Finding{File: "tools/edgerec/report.go", Line: 44, Kind: review.KindBug, Severity: review.SeverityMajor,
			Text: "Ошибка os.WriteFile проигнорирована: верни её вызывающему.", Evidence: review.Evidence{Path: "tools/edgerec/report.go", Line: 44}},
			"Проигнорированная ошибка записи отчёта.")
	case strings.Contains(q, "aitr.ps1"):
		h, ok := hitWith16(req, "без &&")
		return changes(rule15(h, ok, "scripts/aitr.ps1", 14, "&& не работает в PowerShell 5.1: два вызова с проверкой $LASTEXITCODE."), "Синтаксис не для PowerShell 5.1.")
	case strings.Contains(q, "scenario.json"):
		h, ok := hitWith16(req, "expectJS")
		return changes(rule15(h, ok, "demos/02-rag-basics/scenario.json", 7, "expectJS с присваиванием; wait 240 с без cut."), "Сценарий нарушает формат edgerec.")
	case strings.Contains(q, "publish.ps1"):
		h, ok := hitWith16(req, "force-push")
		return changes(rule15(h, ok, "scripts/publish.ps1", 3, "git push --force без разрешения и печать ключа."), "Публикация без разрешения и утечка ключа.")
	}
	return finish15(review.Review{Summary: "Замечаний нет: исправлена опечатка.", Verdict: review.VerdictOK}), nil
}

func TestT15Fake(t *testing.T) {
	d := t15Deps(t)
	set, prs := writePlanted(t)
	fake := &llmtest.Fake{Fn: scenario15}
	var log strings.Builder
	results, err := T15(context.Background(), T15Options{Deps: d, LLM: fake, Model: "fake", Set: set, Planted: prs, Log: &log})
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
	if r.Mechanism != review.RAG || r.Requests != 15 || len(r.Rows) != 15 {
		t.Errorf("стенд: механизм %s, запросов %d, строк %d", r.Mechanism, r.Requests, len(r.Rows))
	}
	got := map[string]float64{}
	for _, m := range r.Metrics {
		if m.Name == "rule найдены" {
			got[m.Lane] = m.Value
		}
	}
	if got["main"] != 3 || got["norag"] != 0 || got["same"] != 3 {
		t.Errorf("rule найдены: %v", got)
	}
	md := Markdown(results)
	for _, want := range []string{Trial15, "пройдено", "review.rag", "посаженные проблемы найдены", "4/4"} {
		if !strings.Contains(md, want) {
			t.Errorf("в отчёте нет %q", want)
		}
	}
	mdPath, jsPath, err := Write(t.TempDir(), "t15", results)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{mdPath, jsPath} {
		if _, err := os.Stat(p); err != nil {
			t.Error(err)
		}
	}
}

func TestT15LoadsSet(t *testing.T) {
	set, prs := writePlanted(t)
	// prs.json с относительными путями патчей — как в eval/review.
	var list []review.Planted
	for _, p := range prs {
		p.Patch = "planted/" + filepath.Base(p.Patch)
		list = append(list, p)
	}
	data, _ := json.Marshal(list)
	if err := os.WriteFile(filepath.Join(set, review.PlantedFile), data, 0o644); err != nil {
		t.Fatal(err)
	}
	d := t15Deps(t)
	results, err := T15(context.Background(), T15Options{Deps: d, LLM: &llmtest.Fake{Fn: scenario15}, Model: "fake", Set: set})
	if err != nil || len(results) != 1 || !results[0].Passed() {
		t.Fatalf("набор из каталога: %v, %+v", err, results)
	}
	if _, err := T15(context.Background(), T15Options{LLM: &llmtest.Fake{}, Set: t.TempDir()}); err == nil {
		t.Fatal("без prs.json — ошибка")
	}
}

func TestConsistentAndOutside(t *testing.T) {
	files, err := review.ParseUnified(t15Patches["P2"])
	if err != nil {
		t.Fatal(err)
	}
	in := review.Input{Files: files}
	clean := review.Planted{ID: "P5", Clean: true}
	planted := review.Planted{ID: "P2", File: "scripts/aitr.ps1", Line: 14, Kind: review.KindRule}
	minor := review.Finding{File: "scripts/aitr.ps1", Line: 14, Kind: review.KindRecommendation, Severity: review.SeverityMinor, Text: "x"}
	major := minor
	major.Severity = review.SeverityMajor
	ok := &review.Turn{Input: in, Review: review.Review{Verdict: review.VerdictOK}}
	withMinor := &review.Turn{Input: in, Review: review.Review{Verdict: review.VerdictChanges, Findings: []review.Finding{minor}}}
	withMajor := &review.Turn{Input: in, Review: review.Review{Verdict: review.VerdictChanges, Findings: []review.Finding{major}}}
	broken := &review.Turn{Input: in, Review: review.Review{Verdict: review.VerdictOK, Findings: []review.Finding{minor}}}
	if !Consistent(clean, ok) || !Consistent(clean, withMinor) || Consistent(clean, withMajor) || Consistent(clean, broken) {
		t.Error("чистый PR")
	}
	if Consistent(planted, ok) || !Consistent(planted, withMajor) || Consistent(planted, broken) {
		t.Error("PR с посадкой")
	}
	outside := review.Finding{File: "scripts/aitr.ps1", Line: 99, Kind: review.KindBug, Severity: review.SeverityMajor, Text: "x"}
	other := review.Finding{File: "docs/x.md", Line: 1, Kind: review.KindBug, Severity: review.SeverityMajor, Text: "x"}
	if n := Outside(&review.Turn{Input: in, Review: review.Review{Findings: []review.Finding{minor, outside, other}}}); n != 2 {
		t.Errorf("вне hunk'ов %d, ожидалось 2", n)
	}
}
