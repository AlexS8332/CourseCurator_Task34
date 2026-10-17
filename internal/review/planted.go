package review

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// PlantedFile — список подставных PR в каталоге eval/review.
const PlantedFile = "prs.json"

// PlantedTolerance — на сколько строк находка может отстоять от
// посаженной и всё ещё считаться найденной.
const PlantedTolerance = 3

// LoadPlanted читает подставные PR стенда: dir/prs.json (массив Planted)
// и патчи, на которые он ссылается (Patch — путь относительно dir, в
// результате — полный). Проверяет: id уникальны, патч существует, у
// непустых (не Clean) есть File, Line > 0 и известный Kind, у Clean —
// нет. Порядок — по id.
func LoadPlanted(dir string) ([]Planted, error) {
	raw, err := os.ReadFile(filepath.Join(dir, PlantedFile))
	if err != nil {
		return nil, err
	}
	var list []Planted
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("%s: %w", PlantedFile, err)
	}
	seen := map[string]bool{}
	for i := range list {
		p := &list[i]
		if p.ID == "" {
			return nil, fmt.Errorf("%s: запись %d без id", PlantedFile, i)
		}
		if seen[p.ID] {
			return nil, fmt.Errorf("%s: id %q повторяется", PlantedFile, p.ID)
		}
		seen[p.ID] = true
		if p.Patch == "" {
			return nil, fmt.Errorf("%s: %s без patch", PlantedFile, p.ID)
		}
		p.Patch = filepath.Join(dir, filepath.FromSlash(p.Patch))
		if abs, err := filepath.Abs(p.Patch); err == nil {
			p.Patch = abs // git apply запускается в корне курса, не здесь
		}
		if _, err := os.Stat(p.Patch); err != nil {
			return nil, fmt.Errorf("%s: %s: патч %s не найден", PlantedFile, p.ID, p.Patch)
		}
		p.File = NormPath(p.File)
		if p.Clean {
			if p.File != "" || p.Line != 0 || p.Kind != "" {
				return nil, fmt.Errorf("%s: %s чистый, но с file/line/kind", PlantedFile, p.ID)
			}
			continue
		}
		if p.File == "" || p.Line <= 0 {
			return nil, fmt.Errorf("%s: %s: нужны file и line", PlantedFile, p.ID)
		}
		switch p.Kind {
		case KindBug, KindRule, KindDocs, KindRecommendation:
		default:
			return nil, fmt.Errorf("%s: %s: неизвестный kind %q", PlantedFile, p.ID, p.Kind)
		}
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	return list, nil
}

// Input — вход ревью из патча подставного PR (заголовок и описание —
// из записи).
func (p Planted) Input() (Input, error) {
	in, err := FromPatch(p.Patch, p.Title)
	if err != nil {
		return Input{}, fmt.Errorf("%s: %w", p.ID, err)
	}
	in.Body = p.Body
	return in, nil
}

// Found — нашло ли ревью посаженную проблему: находка в том же файле,
// не дальше PlantedTolerance строк от посаженной и ожидаемого вида. Из
// нескольких подходящих — ближайшая. У чистого PR искать нечего:
// всегда false.
func (p Planted) Found(r Review) (bool, *Finding) {
	if p.Clean || p.File == "" {
		return false, nil
	}
	var best *Finding
	bestDist := PlantedTolerance + 1
	for i := range r.Findings {
		f := &r.Findings[i]
		if NormPath(f.File) != p.File || !strings.EqualFold(f.Kind, p.Kind) {
			continue
		}
		d := f.Line - p.Line
		if d < 0 {
			d = -d
		}
		if d < bestDist {
			best, bestDist = f, d
		}
	}
	return best != nil, best
}
