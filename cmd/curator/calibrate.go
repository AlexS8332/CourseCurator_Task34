package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"sort"

	"aitr/34-files-agent/internal/kb"
)

func init() {
	register("calibrate", "[-questions eval/help.json] [-write] [-root dir] [-data dir]  порог «не знаю» по dev- и out-вопросам", runCalibrate)
}

// runCalibrate — `curator calibrate`: для каждого вопроса набора с ответом
// (split dev и test) и без ответа (split out) берётся лучший косинус
// плотного поиска; порог — середина между худшим «с ответом» и лучшим «без
// ответа». С -write порог пишется в индекс (kb_indexes.min_score), и Gate
// в docs_search начинает отдавать «только unknown», когда выдача ниже
// порога. Без эмбеддера (BM25) порога нет — шкала у BM25 своя.
func runCalibrate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("calibrate", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "корень курса")
	dataFlag := fs.String("data", "", "каталог данных (CURATOR_DATA или <root>/tools/curator/data)")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	qPath := fs.String("questions", "eval/help.json", "набор вопросов")
	write := fs.Bool("write", false, "записать порог в индекс")
	if err := fs.Parse(args); err != nil {
		return err
	}
	loadDotEnv(*rootFlag)
	qs, err := kb.LoadQuestions(*qPath)
	if err != nil {
		return err
	}
	d, closeDeps, err := curatorDeps(ctx, *rootFlag, *dataFlag, *indexFlag)
	if err != nil {
		return err
	}
	defer closeDeps()
	if d.Searcher == nil {
		return errors.New("нет базы документации — соберите: curator index")
	}
	if d.Searcher.Embedder == nil {
		return errors.New("нет эмбеддера — порог калибруется только для плотного поиска (запустите embedder/server.py)")
	}
	type row struct {
		id, split string
		score     float64
		title     string
	}
	var rows []row
	minIn, maxOut := math.Inf(1), math.Inf(-1)
	for _, q := range qs.Questions {
		if q.Type == "state" {
			continue
		}
		hits, info, err := d.Searcher.Search(ctx, q.Q, kb.SearchOptions{Index: d.Index, K: 1})
		if err != nil {
			return fmt.Errorf("%s: %w", q.ID, err)
		}
		if info.Mode != kb.Dense {
			return fmt.Errorf("%s: поиск прошёл в режиме %s, а не плотном — порог не откалибровать", q.ID, info.Mode)
		}
		r := row{id: q.ID, split: q.Split}
		if len(hits) > 0 {
			r.score = hits[0].Score
			r.title = hits[0].Title + " › " + hits[0].Section
		}
		rows = append(rows, r)
		if q.Answerable {
			minIn = math.Min(minIn, r.score)
		} else {
			maxOut = math.Max(maxOut, r.score)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].score > rows[j].score })
	fmt.Printf("%-5s %-5s %-7s %s\n", "id", "split", "cos", "лучший фрагмент")
	for _, r := range rows {
		fmt.Printf("%-5s %-5s %.4f  %s\n", r.id, r.split, r.score, r.title)
	}
	fmt.Printf("\nхудший с ответом: %.4f · лучший без ответа: %.4f\n", minIn, maxOut)
	threshold := (minIn + maxOut) / 2
	if maxOut >= minIn {
		// Шкалы пересекаются: перебираем середины между соседними баллами и
		// берём порог, при котором отсечено больше всего вопросов без ответа
		// и меньше всего — с ответом (при равенстве — ниже, чтобы не терять
		// ответы). Остальные «не знаю» решает модель.
		best, bestGain := maxOut, math.Inf(-1)
		for i := 0; i+1 < len(rows); i++ {
			cand := (rows[i].score + rows[i+1].score) / 2
			gain := 0.0
			for _, r := range rows {
				if r.score >= cand {
					continue
				}
				if r.split == "out" {
					gain++
				} else {
					gain--
				}
			}
			if gain > bestGain || (gain == bestGain && cand < best) {
				best, bestGain = cand, gain
			}
		}
		threshold = best
		fmt.Printf("шкалы пересекаются: порог %.4f отсекает out-вопросы с перевесом %+.0f, остальные «не знаю» решает модель\n", threshold, bestGain)
	}
	fmt.Printf("порог: %.4f\n", threshold)
	if !*write {
		fmt.Fprintln(os.Stderr, "порог не записан (нужен -write)")
		return nil
	}
	if err := d.Store.SetMinScore(ctx, d.Index, threshold); err != nil {
		return err
	}
	fmt.Printf("порог записан в индекс %s\n", d.Index)
	return nil
}
