package review

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/gitx"
)

// plantedDir — подставные PR стенда И-15.
const plantedDir = "../../eval/review"

// courseRoot — корень курса: CURATOR_COURSE_ROOT или каталог с CLAUDE.md
// и scripts/aitr.ps1 вверх по дереву от пакета; пусто — корня нет
// (чужая машина, снимок).
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

// TestLoadPlanted — состав набора: P1–P5 по порядку, четыре с
// посаженной проблемой и один чистый; у каждого патч читается FromPatch
// и трогает ровно тот файл, что указан.
func TestLoadPlanted(t *testing.T) {
	list, err := LoadPlanted(plantedDir)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	clean := 0
	for _, p := range list {
		ids = append(ids, p.ID)
		if p.Title == "" || p.Note == "" {
			t.Errorf("%s: %+v", p.ID, p)
		}
		if _, err := os.Stat(p.Patch); err != nil {
			t.Errorf("%s: путь патча не полный: %v", p.ID, err)
		}
		in, err := p.Input()
		if err != nil {
			t.Errorf("%s: %v", p.ID, err)
			continue
		}
		if in.Title != p.Title || in.Body != p.Body || len(in.Files) != 1 {
			t.Errorf("%s: вход %+v", p.ID, in)
		}
		if p.Clean {
			clean++
			continue
		}
		if in.Files[0].Path != p.File {
			t.Errorf("%s: патч трогает %s, а посажено в %s", p.ID, in.Files[0].Path, p.File)
		}
	}
	if got := strings.Join(ids, ","); got != "P1,P2,P3,P4,P5" || clean != 1 {
		t.Errorf("набор: %s, чистых %d", got, clean)
	}
	kinds := map[string]string{"P1": KindBug, "P2": KindRule, "P3": KindRule, "P4": KindRule}
	for _, p := range list {
		if want, ok := kinds[p.ID]; ok && p.Kind != want {
			t.Errorf("%s: kind %q, ждал %q", p.ID, p.Kind, want)
		}
	}
	if _, err := LoadPlanted(t.TempDir()); err == nil {
		t.Error("каталог без prs.json — ошибка")
	}
}

// TestPlantedApply — патчи свежие: `git apply --check` каждого в корне
// курса проходит (без корня — Skip), а Planted.Line — комментируемая
// строка патча (новая нумерация) и на ней видна посаженная проблема.
func TestPlantedApply(t *testing.T) {
	list, err := LoadPlanted(plantedDir)
	if err != nil {
		t.Fatal(err)
	}
	// Commentable — не зависит от корня: проверяется всегда.
	marks := map[string]string{"P1": "_ = os.WriteFile", "P2": "&&", "P3": "expectJS", "P4": "git push --force"}
	for _, p := range list {
		if p.Clean {
			continue
		}
		in, err := p.Input()
		if err != nil {
			t.Fatal(err)
		}
		f := in.File(p.File)
		if f == nil {
			t.Errorf("%s: файла %s нет в патче", p.ID, p.File)
			continue
		}
		txt, ok := f.LineText(p.Line)
		if !ok {
			t.Errorf("%s: строка %d не комментируемая (доступны %s)", p.ID, p.Line, f.Ranges())
			continue
		}
		if !strings.Contains(txt, marks[p.ID]) {
			t.Errorf("%s: на строке %d нет %q: %q", p.ID, p.Line, marks[p.ID], txt)
		}
	}

	root := courseRoot(t)
	if root == "" {
		t.Skip("корень курса не найден: задайте CURATOR_COURSE_ROOT")
	}
	if err := gitx.Available(); err != nil {
		t.Skip(err)
	}
	ctx := context.Background()
	repo := gitx.Repo{Dir: root}
	for _, p := range list {
		if _, err := repo.Run(ctx, "apply", "--check", p.Patch); err != nil {
			t.Errorf("%s не ложится на корень %s: %v", p.ID, root, err)
		}
	}
}

func TestPlantedFound(t *testing.T) {
	p := Planted{ID: "P4", File: "scripts/publish.ps1", Line: 12, Kind: KindRule}
	r := Review{Findings: []Finding{
		{File: "scripts/publish.ps1", Line: 14, Kind: KindRule, Text: "ключ"},
		{File: "scripts/publish.ps1", Line: 12, Kind: KindBug, Text: "не тот вид"},
		{File: "b/scripts/publish.ps1", Line: 11, Kind: KindRule, Text: "push"},
		{File: "scripts/other.ps1", Line: 12, Kind: KindRule, Text: "другой файл"},
	}}
	ok, f := p.Found(r)
	if !ok || f == nil || f.Text != "push" {
		t.Errorf("ближайшая подходящая: %v %+v", ok, f)
	}
	far := Review{Findings: []Finding{{File: "scripts/publish.ps1", Line: 16, Kind: KindRule}}}
	if ok, _ := p.Found(far); ok {
		t.Error("дальше 3 строк — не найдено")
	}
	if ok, _ := (Planted{ID: "P5", Clean: true}).Found(r); ok {
		t.Error("у чистого искать нечего")
	}
	if ok, _ := p.Found(Review{}); ok {
		t.Error("пусто")
	}
}
