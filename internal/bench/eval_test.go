package bench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/kb"
)

// helpQuestions — набор И-14.
const helpQuestions = "../../eval/help.json"

// courseRoot — корень курса: CURATOR_COURSE_ROOT или каталог с CLAUDE.md
// вверх по дереву от пакета; пусто — корня нет (чужая машина, снимок).
func courseRoot(t *testing.T) string {
	t.Helper()
	if r := strings.TrimSpace(os.Getenv("CURATOR_COURSE_ROOT")); r != "" {
		return r
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "CLAUDE.md")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "scripts", "aitr.ps1")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// TestHelpQuestionsShape — состав набора: 10 docs/test, 6 docs/dev, 4 state
// (с note ref:…), 4 out; у docs есть evidence и must; id уникальны.
func TestHelpQuestionsShape(t *testing.T) {
	qs, err := kb.LoadQuestions(helpQuestions)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	seen := map[string]bool{}
	for _, q := range qs.Questions {
		if seen[q.ID] {
			t.Errorf("%s: id повторяется", q.ID)
		}
		seen[q.ID] = true
		counts[q.Type+"/"+q.Split]++
		switch q.Type {
		case TypeDocs:
			if len(q.Evidence) == 0 || q.Expect == nil || len(q.Expect.Must) == 0 || !q.Answerable {
				t.Errorf("%s: у docs нужны evidence, expect.must и answerable", q.ID)
			}
		case TypeState:
			if kind, _ := refKind(q); kind == "" {
				t.Errorf("%s: у state нужен note «ref:…»", q.ID)
			}
			if q.Expect != nil && len(q.Expect.Must) > 0 {
				t.Errorf("%s: у state expect пустой — эталон считает код", q.ID)
			}
		case TypeOut:
			if q.Answerable || len(q.Evidence) > 0 || q.Split != kb.SplitOut {
				t.Errorf("%s: out — не answerable, без evidence, split out", q.ID)
			}
		default:
			t.Errorf("%s: неизвестный type %q", q.ID, q.Type)
		}
	}
	want := map[string]int{"docs/test": 10, "docs/dev": 6, "state/dev": 4, "out/out": 4}
	for k, n := range want {
		if counts[k] != n {
			t.Errorf("%s: %d вопросов, ожидалось %d", k, counts[k], n)
		}
	}
}

// TestEvidenceExists — каждая evidence.quote — дословная подстрока файла
// курса по doc_id (после нормализации ё/е, регистра и пробелов, как у
// cite). Без корня курса — пропуск.
func TestEvidenceExists(t *testing.T) {
	root := courseRoot(t)
	if root == "" {
		t.Skip("корня курса нет (CURATOR_COURSE_ROOT не задан, CLAUDE.md не найден)")
	}
	qs, err := kb.LoadQuestions(helpQuestions)
	if err != nil {
		t.Fatal(err)
	}
	cache := map[string]string{}
	for _, q := range qs.Questions {
		for i, e := range q.Evidence {
			if strings.Contains(e.DocID, "\\") || strings.HasPrefix(e.DocID, "/") {
				t.Errorf("%s evidence %d: doc_id — относительный путь с «/»: %q", q.ID, i, e.DocID)
				continue
			}
			text, ok := cache[e.DocID]
			if !ok {
				b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(e.DocID)))
				if err != nil {
					t.Errorf("%s evidence %d: %v", q.ID, i, err)
					continue
				}
				text = corpus.Normalize(string(b))
				cache[e.DocID] = text
			}
			if len([]rune(e.Quote)) < 15 {
				t.Errorf("%s evidence %d: цитата короче 15 символов", q.ID, i)
			}
			if !strings.Contains(text, corpus.Normalize(e.Quote)) {
				t.Errorf("%s evidence %d: цитата не найдена в %s: %q", q.ID, i, e.DocID, e.Quote)
			}
		}
	}
}
