package files

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/review"
)

// Воспроизводимые сценарии eval/files/*.json: цель словами и ожидания,
// которые проверяет код на worktree после прогона. Сам прогон (plan →
// авто-согласие → apply) собирает RunScript из этапов agent.go; здесь —
// загрузка сценария и проверки.

// QuoteMin — короче стольких рун цитата «…» в quotes_verbatim не
// проверяется (это слово или термин, а не цитата).
const QuoteMin = 25

// gitTimeout — предел на команды git внутри проверок.
const gitTimeout = 30 * time.Second

// LoadScript читает сценарий из JSON-файла и проверяет его: id и goal
// непусты, у каждой проверки известный kind и нужные поля, регулярные
// выражения компилируются, пути — относительные без «..».
func LoadScript(path string) (Script, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Script{}, err
	}
	var s Script
	if err := json.Unmarshal(raw, &s); err != nil {
		return Script{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if err := s.Validate(); err != nil {
		return Script{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	return s, nil
}

// Validate — проверка сценария (см. LoadScript). Пути проверок
// приводятся к NormPath.
func (s *Script) Validate() error {
	if strings.TrimSpace(s.ID) == "" {
		return errors.New("пустой id")
	}
	if strings.TrimSpace(s.Goal) == "" {
		return fmt.Errorf("%s: пустая goal", s.ID)
	}
	if s.Expect.FilesMin < 0 {
		return fmt.Errorf("%s: files_min < 0", s.ID)
	}
	for i, a := range s.Expect.FilesAllowed {
		if strings.TrimSpace(a) == "" {
			return fmt.Errorf("%s: files_allowed[%d] пуст", s.ID, i)
		}
	}
	for i := range s.Expect.Checks {
		c := &s.Expect.Checks[i]
		if err := c.validate(); err != nil {
			return fmt.Errorf("%s: checks[%d]: %w", s.ID, i, err)
		}
	}
	return nil
}

func (c *Check) validate() error {
	known := false
	for _, k := range CheckKinds {
		if c.Kind == k {
			known = true
		}
	}
	if !known {
		return fmt.Errorf("неизвестный kind %q (известны: %s)", c.Kind, strings.Join(CheckKinds, ", "))
	}
	if c.Path != "" {
		n := NormPath(c.Path)
		if filepath.IsAbs(c.Path) || n == ".." || strings.HasPrefix(n, "../") {
			return fmt.Errorf("path %q должен быть относительным внутри корня", c.Path)
		}
		c.Path = n
	}
	if c.Pattern != "" {
		if _, err := regexp.Compile(c.Pattern); err != nil {
			return fmt.Errorf("pattern: %w", err)
		}
	}
	if c.Min < 0 || c.Min > 1 {
		return fmt.Errorf("min %v вне [0, 1]", c.Min)
	}
	if c.Range[0] < 0 || c.Range[1] < 0 || (c.Range[1] != 0 && c.Range[0] > c.Range[1]) {
		return fmt.Errorf("range %v: нужен [от, до], от ≤ до", c.Range)
	}
	switch c.Kind {
	case CheckContains, CheckAbsent, CheckRegex:
		if c.Path == "" {
			return errors.New("нужен path")
		}
		if c.Kind == CheckRegex && c.Pattern == "" {
			return errors.New("нужен pattern")
		}
		if c.Text == "" && c.Pattern == "" {
			return errors.New("нужен text или pattern")
		}
	case CheckExists:
		if c.Path == "" {
			return errors.New("нужен path")
		}
	case CheckDiffWithin:
		if c.Path == "" {
			return errors.New("нужен path")
		}
		if c.Text == "" && c.Range[1] == 0 {
			return errors.New("нужен text (заголовок раздела) или range")
		}
	case CheckQuotesVerbatim:
		if c.Path == "" || c.Source == "" {
			return errors.New("нужны path и source")
		}
	case CheckCoverage:
		if c.Pattern == "" || c.Source == "" {
			return errors.New("нужны pattern и source")
		}
	}
	return nil
}

// Allows — можно ли сценарию менять путь: files_allowed пуст — любой.
func (s Script) Allows(rel string) bool {
	if len(s.Expect.FilesAllowed) == 0 {
		return true
	}
	return Allowed(rel, s.Expect.FilesAllowed)
}

// Checks — все проверки сценария на корне root (worktree прогона).
// Проверке coverage без text и path подставляется summary (итог
// files_done).
func Checks(root string, s Script, summary string) []CheckResult {
	out := make([]CheckResult, 0, len(s.Expect.Checks))
	for _, c := range s.Expect.Checks {
		if c.Kind == CheckCoverage && c.Text == "" && c.Path == "" {
			c.Text = summary
		}
		out = append(out, Evaluate(root, c))
	}
	return out
}

// Verify — ожидания сценария по прогону: затронуто файлов ≥ files_min,
// все изменённые пути внутри files_allowed, план с записью или без —
// как в expect. Результаты — в виде CheckResult с Kind «expect» и
// Text — именем ожидания.
func Verify(r *Run, s Script) []CheckResult {
	var out []CheckResult
	add := func(name string, ok bool, got string) {
		out = append(out, CheckResult{Check: Check{Kind: "expect", Text: name}, OK: ok, Got: got})
	}
	n := 0
	if r != nil {
		n = len(r.Changed)
	}
	add("files_min", n >= s.Expect.FilesMin, fmt.Sprintf("затронуто %d, нужно ≥ %d", n, s.Expect.FilesMin))
	var bad []string
	if r != nil {
		for _, p := range r.Changed {
			if !s.Allows(p) {
				bad = append(bad, p)
			}
		}
	}
	if len(bad) == 0 {
		add("files_allowed", true, "все изменённые пути разрешены")
	} else {
		add("files_allowed", false, "вне files_allowed: "+strings.Join(bad, ", "))
	}
	switch {
	case r == nil || r.Plan == nil:
		add("writes", false, "плана нет")
	case r.Plan.Writes != s.Expect.Writes:
		add("writes", false, fmt.Sprintf("в плане writes=%v, ожидалось %v", r.Plan.Writes, s.Expect.Writes))
	default:
		add("writes", true, fmt.Sprintf("writes=%v", r.Plan.Writes))
	}
	return out
}

// AllOK — все ли проверки пройдены.
func AllOK(rs []CheckResult) bool {
	for _, r := range rs {
		if !r.OK {
			return false
		}
	}
	return true
}

// Evaluate — одна проверка на корне root. Никогда не паникует: ошибка
// чтения или git — не пройдено с объяснением в Got.
func Evaluate(root string, c Check) CheckResult {
	res := CheckResult{Check: c}
	var ok bool
	var got string
	switch c.Kind {
	case CheckExists:
		ok, got = evalExists(root, c)
	case CheckContains, CheckRegex:
		ok, got = evalContains(root, c, true)
	case CheckAbsent:
		ok, got = evalContains(root, c, false)
	case CheckDiffWithin:
		ok, got = evalDiffWithin(root, c)
	case CheckQuotesVerbatim:
		ok, got = evalQuotes(root, c)
	case CheckCoverage:
		ok, got = evalCoverage(root, c)
	default:
		ok, got = false, "неизвестный kind "+c.Kind
	}
	res.OK, res.Got = ok, got
	return res
}

func evalExists(root string, c Check) (bool, string) {
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(c.Path)))
	switch {
	case err != nil:
		return false, "файла " + c.Path + " нет"
	case st.IsDir():
		return false, c.Path + " — каталог"
	}
	return true, fmt.Sprintf("%s: %d байт", c.Path, st.Size())
}

// evalContains — contains/regex (want=true) и absent (want=false).
func evalContains(root string, c Check, want bool) (bool, string) {
	text, err := readText(root, c.Path)
	if err != nil {
		return false, "файла " + c.Path + " нет"
	}
	line, found := findIn(text, c)
	what := c.Text
	if c.Pattern != "" {
		what = "/" + c.Pattern + "/"
	}
	switch {
	case found && want:
		return true, fmt.Sprintf("%s: строка %d", what, line)
	case found && !want:
		return false, fmt.Sprintf("найдено %s: строка %d, а не должно", what, line)
	case !found && want:
		return false, fmt.Sprintf("в %s нет %s", c.Path, what)
	}
	return true, fmt.Sprintf("в %s нет %s", c.Path, what)
}

// findIn — где в тексте Text (подстрока) или Pattern: номер строки с 1.
func findIn(text string, c Check) (int, bool) {
	var at int
	if c.Pattern != "" {
		re, err := regexp.Compile(c.Pattern)
		if err != nil {
			return 0, false
		}
		loc := re.FindStringIndex(text)
		if loc == nil {
			return 0, false
		}
		at = loc[0]
	} else {
		at = strings.Index(text, c.Text)
		if at < 0 {
			return 0, false
		}
	}
	return strings.Count(text[:at], "\n") + 1, true
}

// evalDiffWithin — изменения файла только внутри раздела (заголовок в
// Text) или строк Range. Diff — незакоммиченные изменения worktree, а
// если дерево чистое — последний коммит (HEAD~1..HEAD). Удалённые
// строки сверяются со старой нумерацией и разделом старого файла,
// добавленные — с новой.
func evalDiffWithin(root string, c Check) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), gitTimeout)
	defer cancel()
	g := gitx.Repo{Dir: root}
	base := "HEAD"
	st, err := g.Status(ctx)
	if err != nil {
		return false, "git: " + err.Error()
	}
	var diff string
	if len(st) > 0 {
		diff, _, err = g.DiffAll(ctx, 0)
	} else {
		base = "HEAD~1"
		if _, e := g.Run(ctx, "rev-parse", "--verify", "-q", "HEAD~1"); e != nil {
			return false, c.Path + " не изменён (дерево чистое, один коммит)"
		}
		diff, _, err = g.Diff(ctx, "HEAD~1", 0)
	}
	if err != nil {
		return false, "git diff: " + err.Error()
	}
	fds, err := review.ParseUnified(diff)
	if err != nil {
		return false, "diff не разобран: " + err.Error()
	}
	fd := review.FileByPath(fds, c.Path)
	if fd == nil {
		return false, c.Path + " не изменён"
	}
	newText, err := readText(root, c.Path)
	if err != nil {
		return false, "файла " + c.Path + " нет"
	}
	oldRaw, err := g.Run(ctx, "show", base+":"+c.Path)
	if err != nil {
		oldRaw = "" // файла в базе не было
	}
	oldText := strings.ReplaceAll(oldRaw, "\r\n", "\n")
	var oldFrom, oldTo, newFrom, newTo int
	if c.Range[1] > 0 {
		oldFrom, oldTo, newFrom, newTo = c.Range[0], c.Range[1], c.Range[0], c.Range[1]
	} else {
		newFrom, newTo = sectionRange(newText, c.Text)
		if newFrom == 0 {
			return false, fmt.Sprintf("в %s нет раздела %q", c.Path, c.Text)
		}
		oldFrom, oldTo = sectionRange(oldText, c.Text)
		if oldFrom == 0 {
			oldFrom, oldTo = newFrom, newTo // раздела не было — удалять из него нечего
		}
	}
	var bad []string
	changed := 0
	for _, h := range fd.Hunks {
		for _, l := range h.Lines {
			switch l.Kind {
			case '+':
				changed++
				if l.New < newFrom || l.New > newTo {
					bad = append(bad, fmt.Sprintf("+%d", l.New))
				}
			case '-':
				changed++
				if l.Old < oldFrom || l.Old > oldTo {
					bad = append(bad, fmt.Sprintf("-%d", l.Old))
				}
			}
		}
	}
	if changed == 0 {
		return false, c.Path + ": в diff нет изменённых строк"
	}
	if len(bad) > 0 {
		if len(bad) > 10 {
			bad = append(bad[:10], "…")
		}
		return false, fmt.Sprintf("вне раздела (строки %d–%d): %s", newFrom, newTo, strings.Join(bad, ", "))
	}
	return true, fmt.Sprintf("%d изменённых строк внутри %d–%d", changed, newFrom, newTo)
}

// sectionRange — строки раздела markdown с заголовком heading (с 1,
// включительно, с самим заголовком) до следующего заголовка того же или
// более высокого уровня; 0, 0 — заголовка нет. Заголовок сравнивается
// без пробелов по краям.
func sectionRange(text, heading string) (int, int) {
	heading = strings.TrimSpace(heading)
	level := headingLevel(heading)
	if level == 0 {
		return 0, 0
	}
	lines := strings.Split(text, "\n")
	from := 0
	for i, l := range lines {
		if strings.TrimSpace(l) == heading {
			from = i + 1
			break
		}
	}
	if from == 0 {
		return 0, 0
	}
	to := len(lines)
	if strings.HasSuffix(text, "\n") {
		to-- // последний элемент Split — пустой хвост
	}
	for i := from; i < len(lines); i++ {
		if lv := headingLevel(strings.TrimSpace(lines[i])); lv > 0 && lv <= level {
			to = i
			break
		}
	}
	return from, to
}

// headingLevel — число «#» заголовка markdown (0 — не заголовок).
func headingLevel(l string) int {
	n := 0
	for n < len(l) && l[n] == '#' {
		n++
	}
	if n == 0 || n > 6 || n >= len(l) || l[n] != ' ' {
		return 0
	}
	return n
}

var quoteRe = regexp.MustCompile(`«([^«»]+)»`)

// evalQuotes — каждая цитата «…» (≥ QuoteMin рун; Range[0] > 0 — свой
// порог) из файла Path — подстрока одного из файлов Source после
// corpus.Normalize. Доля дословных ≥ Min (0 — все); цитат нет — не
// пройдено.
func evalQuotes(root string, c Check) (bool, string) {
	text, err := readText(root, c.Path)
	if err != nil {
		return false, "файла " + c.Path + " нет"
	}
	minLen := QuoteMin
	if c.Range[0] > 0 {
		minLen = c.Range[0]
	}
	var quotes []string
	for _, m := range quoteRe.FindAllStringSubmatch(text, -1) {
		q := strings.TrimSpace(m[1])
		if utf8.RuneCountInString(q) >= minLen {
			quotes = append(quotes, q)
		}
	}
	if len(quotes) == 0 {
		return false, fmt.Sprintf("в %s нет цитат «…» длиннее %d символов", c.Path, minLen)
	}
	srcs, err := sources(root, c.Source)
	if err != nil {
		return false, "source: " + err.Error()
	}
	if len(srcs) == 0 {
		return false, "source " + c.Source + ": файлов нет"
	}
	norms := make([]string, 0, len(srcs))
	for _, s := range srcs {
		t, err := readText(root, s)
		if err != nil {
			continue
		}
		norms = append(norms, normQuote(t))
	}
	okN := 0
	var bad []string
	for _, q := range quotes {
		nq := normQuote(q)
		found := false
		for _, n := range norms {
			if strings.Contains(n, nq) {
				found = true
				break
			}
		}
		if found {
			okN++
		} else if len(bad) < 5 {
			bad = append(bad, "«"+shorten(q, 60)+"»")
		}
	}
	share := float64(okN) / float64(len(quotes))
	thr := c.Min
	if thr == 0 {
		thr = 1
	}
	got := fmt.Sprintf("дословно %d из %d (%.0f %%)", okN, len(quotes), share*100)
	if len(bad) > 0 {
		got += "; не найдено: " + strings.Join(bad, ", ")
	}
	return share >= thr, got
}

// evalCoverage — эталон: файлы Source, в которых есть Pattern; найдено:
// те из них, чей путь или имя (с расширением или без) упомянуты в Text
// (пусто — в содержимом файла Path). Доля ≥ Min (0 — все).
func evalCoverage(root string, c Check) (bool, string) {
	re, err := regexp.Compile(c.Pattern)
	if err != nil {
		return false, "pattern: " + err.Error()
	}
	text := c.Text
	if text == "" && c.Path != "" {
		t, err := readText(root, c.Path)
		if err != nil {
			return false, "файла " + c.Path + " нет"
		}
		text = t
	}
	srcs, err := sources(root, c.Source)
	if err != nil {
		return false, "source: " + err.Error()
	}
	var ref, missing []string
	for _, s := range srcs {
		t, err := readText(root, s)
		if err != nil || !re.MatchString(t) {
			continue
		}
		ref = append(ref, s)
		base := filepath.Base(s)
		stem := strings.TrimSuffix(base, filepath.Ext(base))
		if !(strings.Contains(text, s) || strings.Contains(text, base) || (stem != "" && strings.Contains(text, stem))) {
			missing = append(missing, s)
		}
	}
	if len(ref) == 0 {
		return false, "эталон пуст: ни в одном файле " + c.Source + " нет /" + c.Pattern + "/"
	}
	found := len(ref) - len(missing)
	share := float64(found) / float64(len(ref))
	thr := c.Min
	if thr == 0 {
		thr = 1
	}
	got := fmt.Sprintf("найдено %d из %d (%.0f %%)", found, len(ref), share*100)
	if len(missing) > 0 {
		got += "; не упомянуты: " + strings.Join(missing, ", ")
	}
	return share >= thr, got
}

// citeRe — строка вида «путь:строка — цитата» (маркер списка, обратные
// кавычки и кавычки цитаты необязательны).
var citeRe = regexp.MustCompile("(?m)^[ \\t]*(?:[-*]|\\d+[.)])?[ \\t]*`?([\\w./\\-]+\\.\\w+)`?:(\\d+)[ \\t]*[—–:-]+[ \\t]*(.+?)[ \\t]*$")

// Citations — проверка ответа без записи (F1): каждая строка «файл:строка
// — цитата» указывает на существующий файл, строка есть и содержит
// цитату (после corpus.Normalize; кавычки и обратные кавычки по краям
// снимаются). Возвращает число ссылок, число верных и объяснения
// неверных.
func Citations(root, text string) (total, ok int, bad []string) {
	for _, m := range citeRe.FindAllStringSubmatch(text, -1) {
		total++
		rel, lineNo, quote := NormPath(m[1]), m[2], strings.Trim(m[3], "«»\"'` ")
		n, _ := strconv.Atoi(lineNo)
		t, err := readText(root, rel)
		if err != nil {
			bad = append(bad, rel+": файла нет")
			continue
		}
		lines := strings.Split(strings.TrimSuffix(t, "\n"), "\n")
		if n < 1 || n > len(lines) {
			bad = append(bad, fmt.Sprintf("%s:%d: в файле %d строк", rel, n, len(lines)))
			continue
		}
		nq := normQuote(quote)
		if nq == "" || !strings.Contains(normQuote(lines[n-1]), nq) {
			bad = append(bad, fmt.Sprintf("%s:%d: строка не содержит «%s»", rel, n, shorten(quote, 40)))
			continue
		}
		ok++
	}
	return total, ok, bad
}

// normQuote — нормализация для сверки цитат: corpus.Normalize (регистр,
// ё/е, кавычки, тире, пробелы) плюс снятые обратные кавычки и звёздочки
// markdown: цитата из карточки с **выделением** или `кодом` дословна и
// без них.
func normQuote(s string) string {
	return corpus.Normalize(strings.NewReplacer("`", "", "*", "").Replace(s))
}

// sources — файлы по образцам через запятую (filepath.Glob от корня),
// пути с «/» от корня, по алфавиту без повторов.
func sources(root, globs string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, g := range strings.Split(globs, ",") {
		g = strings.TrimSpace(g)
		if g == "" {
			continue
		}
		matches, err := filepath.Glob(filepath.Join(root, filepath.FromSlash(g)))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", g, err)
		}
		for _, m := range matches {
			rel, err := filepath.Rel(root, m)
			if err != nil {
				continue
			}
			if st, err := os.Stat(m); err != nil || st.IsDir() {
				continue
			}
			rel = filepath.ToSlash(rel)
			if !seen[rel] {
				seen[rel] = true
				out = append(out, rel)
			}
		}
	}
	sort.Strings(out)
	return out, nil
}

// readText — текст файла от корня: CRLF → LF, без BOM.
func readText(root, rel string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(strings.ReplaceAll(string(raw), "\r\n", "\n"), string(rune(0xFEFF))), nil
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
