package review

import (
	"strings"
	"testing"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
)

func markdownTurn(t *testing.T) *Turn {
	t.Helper()
	in := testInput(t)
	hits := []kb.Hit{{Chunk: kb.Chunk{ID: "CLAUDE.md/structure/001", DocID: "CLAUDE.md", Title: "Правила", Path: []string{"Инструменты"}, Text: ruleShell}}}
	return &Turn{Input: in, Hits: hits,
		Review: Review{Verdict: VerdictChanges, Summary: "Одно нарушение правил и один баг.", Findings: []Finding{
			{File: "scripts/aitr.ps1", Line: 15, Kind: KindBug, Severity: SeverityMinor, Text: "Возврат без проверки.", Evidence: Evidence{Path: "scripts/aitr.ps1", Line: 30}},
			{File: "scripts/aitr.ps1", Line: 14, Kind: KindRule, Severity: SeverityMajor, Text: "&& не для PowerShell 5.1.",
				Evidence: Evidence{ChunkID: "CLAUDE.md/structure/001", Quote: "без &&, ||, тернарников и ??"}},
			{File: "scripts/aitr.ps1", Line: 99, Kind: KindRecommendation, Severity: SeverityMinor, Text: "Вне diff."},
			{File: "docs/x.md", Line: 1, Kind: KindRecommendation, Severity: SeverityMinor, Text: "Файла нет в diff."},
		}},
		Stats: agent.Stats{Steps: 2, Usage: llm.Usage{Total: 1234}, Cost: llm.Cost{Known: true, USD: 0.0012}},
	}
}

func TestMarkdown(t *testing.T) {
	turn := markdownTurn(t)
	md := turn.Markdown()
	for _, want := range []string{
		"## Ревью: нужны правки — находок 4 (major 1, minor 3)",
		"### scripts/aitr.ps1\n- **строка 14 · правило · major** — && не для PowerShell 5.1.\n  > «без &&, ||, тернарников и ??» — CLAUDE.md › Инструменты",
		"- **строка 15 · баг · minor** — Возврат без проверки. (см. scripts/aitr.ps1:30)",
		"- **строка 99 · рекомендация · minor**",
		"### docs/x.md",
		"**Сводка.** Одно нарушение правил и один баг.",
		"_запросов 2 · токенов 1234 · $0.0012_",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("в markdown нет %q:\n%s", want, md)
		}
	}
	// Файл из diff идёт раньше чужого; маркера дедупа в тексте нет.
	if strings.Index(md, "### scripts/aitr.ps1") > strings.Index(md, "### docs/x.md") || strings.Contains(md, "<!--") {
		t.Errorf("порядок файлов или маркер:\n%s", md)
	}
	turn.Unverified, turn.Check = true, cite.CiteCheck{Problems: []string{"строка 99 не в diff"}}
	if md := turn.Markdown(); !strings.Contains(md, "_Не проверено: строка 99 не в diff._") {
		t.Errorf("пометка «не проверено»:\n%s", md)
	}
	turn.Stats.Cost.Known = false
	if !strings.Contains(turn.StatsLine(), "· ?_") {
		t.Errorf("цена без прайса: %s", turn.StatsLine())
	}
}

func TestComments(t *testing.T) {
	turn := markdownTurn(t)
	cs := turn.Comments()
	if len(cs) != 2 {
		t.Fatalf("комментариев %d, ожидалось 2 (только Commentable): %+v", len(cs), cs)
	}
	if cs[0].Line != 15 || !strings.Contains(cs[0].Body, "**баг · minor**") || !strings.Contains(cs[0].Body, "(см. scripts/aitr.ps1:30)") {
		t.Errorf("комментарий bug: %+v", cs[0])
	}
	if cs[1].Line != 14 || cs[1].Path != "scripts/aitr.ps1" || !strings.Contains(cs[1].Body, "> «без &&, ||, тернарников и ??» — CLAUDE.md › Инструменты") {
		t.Errorf("комментарий rule: %+v", cs[1])
	}
	var empty *Turn
	if empty.Markdown() != "" || empty.Comments() != nil {
		t.Error("nil Turn")
	}
}

func TestSourceLabel(t *testing.T) {
	hits := []kb.Hit{
		{Chunk: kb.Chunk{ID: "a", DocID: "tools/edgerec/README.md", Path: []string{"Сценарий", "expectJS"}}},
		{Chunk: kb.Chunk{ID: "b", Title: "FAQ", Section: "Ключи"}},
		{Chunk: kb.Chunk{ID: "c", DocID: "docs/faq.md"}},
	}
	for id, want := range map[string]string{"a": "tools/edgerec/README.md › Сценарий › expectJS", "b": "FAQ › Ключи", "c": "docs/faq.md", "zzz": "zzz"} {
		if got := SourceLabel(id, hits); got != want {
			t.Errorf("%s: %q, ожидалось %q", id, got, want)
		}
	}
}
