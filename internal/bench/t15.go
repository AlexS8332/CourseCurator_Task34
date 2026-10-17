package bench

import (
	"context"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"time"

	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/review"
)

// И-15 — «ревью PR корня с опорой на регламент» (день 32).
//
// Набор eval/review: prs.json и planted/P1..P5.patch — четыре подставных
// PR с посаженной проблемой (P1 — bug, видно без документации; P2–P4 —
// rule: PowerShell 5.1, сценарий edgerec, push и ключи) и один чистый
// (P5). Вход каждого — review.FromPatch. Один стенд, три дорожки,
// отличающиеся ровно одним механизмом (ИП-13): основная / без базы
// (review.rag выкл) / повтор для шума.
//
// Истина — файл, строка и вид посаженной проблемы (review.Planted.Found:
// тот же файл, |line − Line| ≤ 3, ожидаемый kind). Пороги — в проверках
// ниже; контроль «без базы» — на rule-PR находок с дословной цитатой
// правила с базой больше, чем без неё, сверх шума между основной дорожкой
// и повтором (само «найдено» без базы не падает: нарушения видны и по
// знаниям модели), а bug находится на обеих.

// Trial15 — имя испытания.
const Trial15 = "И-15"

// MaxStepsReview — запросов к модели на ход ревью в среднем.
const MaxStepsReview = 6.0

// Типы PR в отчёте (Row.Type).
const (
	PRPlanted = "planted"
	PRClean   = "clean"
)

// T15Options — параметры прогона.
type T15Options struct {
	Deps  project.Deps
	LLM   llm.Chatter
	Model string
	// Set — каталог набора (eval/review с prs.json и planted/*.patch).
	Set string
	// Planted — подставные PR вместо review.LoadPlanted(Set) (тесты); у
	// каждого Patch — полный путь к патчу.
	Planted []review.Planted
	// Log — ход прогона строками; nil — молча.
	Log io.Writer
}

// T15 гоняет стенд И-15 и возвращает его результат (один стенд — один
// элемент, как у T16).
func T15(ctx context.Context, o T15Options) ([]*Result, error) {
	if o.LLM == nil {
		return nil, fmt.Errorf("нет модели")
	}
	prs := o.Planted
	if len(prs) == 0 {
		if strings.TrimSpace(o.Set) == "" {
			return nil, fmt.Errorf("не указан каталог набора (eval/review)")
		}
		var err error
		if prs, err = review.LoadPlanted(o.Set); err != nil {
			return nil, fmt.Errorf("набор %s: %w", o.Set, err)
		}
	}
	if len(prs) == 0 {
		return nil, fmt.Errorf("в наборе нет подставных PR")
	}
	reg := features.Catalog()
	base := reg.Defaults()
	lanes := []Lane{
		{Name: "main", Features: base, Note: "основная: docs_search по регламенту и карточкам"},
		{Name: "norag", Features: base.With(review.RAG, false), Note: "без базы документации (read_file, repo_search, git_log остаются)"},
		{Name: "same", Features: base, Same: true},
	}
	mech, err := CheckLanes(reg, lanes)
	if err != nil {
		return nil, err
	}
	r := &Result{Trial: Trial15, Stand: "prs", Mechanism: mech, Model: o.Model, Started: time.Now(), Lanes: lanes}
	if o.Log != nil {
		fmt.Fprintf(o.Log, "%s · стенд prs: %d PR × %d дорожек; набор %s\n", Trial15, len(prs), len(lanes), orDash(o.Set))
	}
	runs := map[string]*lane15{}
	for _, l := range lanes {
		lr, err := run15(ctx, o, l, prs, r)
		if err != nil {
			return []*Result{r}, err
		}
		runs[l.Name] = lr
		r.Cost = r.Cost.Add(lr.cost)
		r.Requests += lr.reqs
		r.Rows = append(r.Rows, lr.rows...)
	}
	r.Seconds = time.Since(r.Started).Seconds()

	tl := map[string]tally15{}
	for name, lr := range runs {
		tl[name] = lr.tally
	}
	m, same, norag := tl["main"], tl["same"], tl["norag"]
	mr := runs["main"]
	needFound := int(math.Ceil(0.75 * float64(m.plantedN)))
	r.check("посаженные проблемы найдены", "main", m.found >= needFound, ratio(m.found, m.plantedN), "≥ "+ratio(needFound, m.plantedN))
	r.check("bug (P1) найден", "main", m.bugFound == m.bugN, ratio(m.bugFound, m.bugN), ratio(m.bugN, m.bugN))
	r.check("bug (P1) найден без базы", "norag", norag.bugFound == norag.bugN, ratio(norag.bugFound, norag.bugN), ratio(norag.bugN, norag.bugN))
	// Контроль «без базы»: нарушения правил модель видит и по своим знаниям
	// (`&&` в PowerShell, force-push), поэтому «найдено» без базы не падает —
	// это отчётная метрика. Что даёт механизм измеримо — находка rule с
	// дословной цитатой правила проекта: без docs_search её быть не может,
	// а разница должна превышать шум между основной дорожкой и повтором.
	noise := abs(m.ruleCited - same.ruleCited)
	r.check("rule (P2–P4): с цитатой правила — с базой больше, чем без, сверх шума", "norag", m.ruleCited-norag.ruleCited > noise,
		fmt.Sprintf("main %s, norag %s, повтор %s (шум %d)", ratio(m.ruleCited, m.ruleN), ratio(norag.ruleCited, norag.ruleN), ratio(same.ruleCited, same.ruleN), noise), "разница > шум")
	r.metric("rule найдены: падение без базы", "norag", float64(m.ruleFound-norag.ruleFound), "", fmt.Sprintf("шум %d — отчётно", abs(m.ruleFound-same.ruleFound)))
	r.check("чистый PR (P5): major", "main", m.cleanMajor <= 1, strconv.Itoa(m.cleanMajor), "≤ 1")
	r.check("находок вне hunk'ов", "main", m.outside == 0, strconv.Itoa(m.outside), "0")
	r.check("вердикт согласован с посадкой", "main", m.consistent == m.n, ratio(m.consistent, m.n), ratio(m.n, m.n))
	r.check("«не проверено»", "main", m.unverified == 0, strconv.Itoa(m.unverified), "0")
	avg := float64(mr.steps) / float64(max(m.n, 1))
	r.check("запросов на ход (среднее)", "main", avg <= MaxStepsReview, trimFloat(avg), fmt.Sprintf("≤ %s", trimFloat(MaxStepsReview)))
	for _, name := range sortedKeys(tl) {
		t, lr := tl[name], runs[name]
		r.metric("посаженные найдены", name, float64(t.found), "", ratio(t.found, t.plantedN))
		r.metric("bug найден", name, float64(t.bugFound), "", ratio(t.bugFound, t.bugN))
		r.metric("rule найдены", name, float64(t.ruleFound), "", ratio(t.ruleFound, t.ruleN))
		r.metric("rule с цитатой документа", name, float64(t.ruleCited), "", ratio(t.ruleCited, t.ruleN))
		r.metric("чистый PR: major", name, float64(t.cleanMajor), "", "")
		r.metric("чистый PR: verdict ok", name, float64(t.cleanOK), "", ratio(t.cleanOK, t.cleanN))
		r.metric("вердикт согласован", name, float64(t.consistent), "", ratio(t.consistent, t.n))
		r.metric("находок всего", name, float64(t.findings), "", "")
		r.metric("находок вне hunk'ов", name, float64(t.outside), "", "")
		r.metric("отказов финишёра", name, float64(t.rejected), "", "")
		r.metric("«не проверено»", name, float64(t.unverified), "", "")
		r.metric("запросов на ход", name, float64(lr.steps)/float64(max(t.n, 1)), "", "")
		r.metric("постоянная часть", name, float64(lr.constant), "токенов", "наибольшая за дорожку")
		r.metric("цена", name, lr.cost.USD, "$", lr.cost.Tariff)
		r.metric("время", name, lr.secs, "с", "")
	}
	return []*Result{r}, nil
}

// tally15 — счётчики одной дорожки.
type tally15 struct {
	n, plantedN, found, bugN, bugFound, ruleN, ruleFound, ruleCited int
	cleanN, cleanOK, cleanMajor, consistent                         int
	findings, outside, rejected, unverified                         int
}

// lane15 — ходы одной дорожки.
type lane15 struct {
	lane     Lane
	tally    tally15
	turns    map[string]*review.Turn
	rows     []Row
	cost     llm.Cost
	reqs     int
	steps    int
	constant int
	secs     float64
}

// run15 — все подставные PR по дорожке.
func run15(ctx context.Context, o T15Options, lane Lane, prs []review.Planted, r *Result) (*lane15, error) {
	lr := &lane15{lane: lane, turns: map[string]*review.Turn{}}
	for _, p := range prs {
		in, err := p.Input()
		if err != nil {
			return nil, fmt.Errorf("дорожка «%s»: %w", lane.Name, err)
		}
		started := time.Now()
		t, err := review.Run(ctx, o.LLM, o.Deps, in, nil, review.Options{Model: o.Model, Features: lane.Features})
		if t == nil {
			return nil, fmt.Errorf("дорожка «%s», %s: %w", lane.Name, p.ID, err)
		}
		secs := time.Since(started).Seconds()
		lr.turns[p.ID] = t
		lr.cost = lr.cost.Add(t.Stats.Cost)
		lr.reqs += t.Stats.Steps
		lr.steps += t.Stats.Steps
		lr.secs += secs
		if c := t.Stats.Context.Estimate.Constant; c > lr.constant {
			lr.constant = c
		}
		kind := PRPlanted
		if p.Clean {
			kind = PRClean
		}
		row := Row{Lane: lane.Name, ID: p.ID, Type: kind, Question: p.Title, Status: t.Review.Verdict,
			Answer: t.Review.Summary, OK: !t.Unverified && err == nil, Steps: t.Stats.Steps, Seconds: secs}
		if err != nil {
			row.Problems = append(row.Problems, err.Error())
			r.Notes = append(r.Notes, fmt.Sprintf("дорожка «%s», %s: ход оборвался: %v", lane.Name, p.ID, err))
		}
		row.Problems = append(row.Problems, t.Check.Problems...)
		for _, c := range t.Calls {
			if !c.Final {
				row.Calls = append(row.Calls, c.Tool)
			}
		}
		lr.rows = append(lr.rows, row)
		count15(&lr.tally, p, t)
		if o.Log != nil {
			found, _ := p.Found(t.Review)
			mark := "—"
			if found {
				mark = "найдено"
			} else if p.Clean {
				mark = "чистый"
			}
			fmt.Fprintf(o.Log, "  %-6s %-4s %-8s %d шаг. %-8s %s\n", lane.Name, p.ID, orDash(t.Review.Verdict), t.Stats.Steps, mark, cell(t.Review.Summary, 60))
		}
	}
	return lr, nil
}

// count15 — счётчики по одному ходу.
func count15(t *tally15, p review.Planted, turn *review.Turn) {
	t.n++
	t.rejected += turn.Rejected
	if turn.Unverified {
		t.unverified++
	}
	t.findings += len(turn.Review.Findings)
	t.outside += Outside(turn)
	major := 0
	for _, f := range turn.Review.Findings {
		if f.Severity == review.SeverityMajor {
			major++
		}
	}
	if p.Clean {
		t.cleanN++
		if turn.Review.Verdict == review.VerdictOK {
			t.cleanOK++
		}
		if major > t.cleanMajor {
			t.cleanMajor = major
		}
		if Consistent(p, turn) {
			t.consistent++
		}
		return
	}
	t.plantedN++
	found, f := p.Found(turn.Review)
	if found {
		t.found++
	}
	switch p.Kind {
	case review.KindBug:
		t.bugN++
		if found {
			t.bugFound++
		}
	case review.KindRule, review.KindDocs:
		t.ruleN++
		if found {
			t.ruleFound++
			// Цитата считается только по фрагменту, который docs_search
			// действительно выдал в этом ходе (turn.Hits): без базы
			// финишёр ссылки не проверяет, и chunk_id может быть сочинён.
			if f != nil && f.Evidence.Quote != "" && issuedHit(turn, f.Evidence.ChunkID) {
				t.ruleCited++
			}
		}
	}
	if Consistent(p, turn) {
		t.consistent++
	}
}

// Outside — сколько находок хода стоят вне hunk'ов diff (не Commentable):
// после отказов финишёра их должно быть 0.
func Outside(t *review.Turn) int {
	n := 0
	for _, f := range t.Review.Findings {
		fd := review.FileByPath(t.Input.Files, f.File)
		if fd == nil || !fd.Commentable(f.Line) {
			n++
		}
	}
	return n
}

// Consistent — вердикт согласован с посадкой: у чистого PR — ok или
// только minor-находки (рекомендации не блокируют слияние); у PR с
// посаженной проблемой — changes; и всегда ok ⇔ находок нет.
func Consistent(p review.Planted, t *review.Turn) bool {
	v := t.Review.Verdict
	if (v == review.VerdictOK) != (len(t.Review.Findings) == 0) {
		return false
	}
	if !p.Clean {
		return v == review.VerdictChanges
	}
	for _, f := range t.Review.Findings {
		if f.Severity == review.SeverityMajor {
			return false
		}
	}
	return v == review.VerdictOK || v == review.VerdictChanges
}

// issuedHit — выдавал ли docs_search фрагмент с таким chunk_id в этом ходе.
func issuedHit(turn *review.Turn, chunkID string) bool {
	if chunkID == "" || turn == nil {
		return false
	}
	for _, h := range turn.Hits {
		if h.ID == chunkID {
			return true
		}
	}
	return false
}
