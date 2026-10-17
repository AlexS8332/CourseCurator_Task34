package web

// Вкладка «Ревью» (день 32): список подставных PR стенда плюс «текущая
// ветка», ревью одного PR ходом агента по SSE (журнал + ответ, как
// /api/ask) и панель «последний прогон Action» из examples/review/ci-*.json.
//
// Окно не знает устройства пакета review: список подставных PR и ход
// приходят замыканиями (serve.go подставляет review.LoadPlanted и
// review.Run, тесты — подставки), diff и находки — своими типами окна,
// чтобы страница и её тесты не зависели от агента. «Посаженное найдено»
// считает код по правилу стенда: тот же файл, |строка − посаженная| ≤ 3,
// ожидаемый вид находки.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/tools"
)

// DefaultRange — диапазон «текущей ветки», если сервер не задал свой.
const DefaultRange = "main...HEAD"

// PlantedTolerance — на сколько строк находка может отстоять от
// посаженной, чтобы считаться найденной.
const PlantedTolerance = 3

// ReviewRequest — тело POST /api/review/run: подставной PR по id или
// диапазон git (пусто — диапазон сервера).
type ReviewRequest struct {
	Planted string `json:"planted,omitempty"`
	Range   string `json:"range,omitempty"`
}

// DiffLine — строка hunk'а: kind ctx|add|del, номера в старой и новой
// нумерации (0 — нет).
type DiffLine struct {
	Kind string `json:"kind"`
	Old  int    `json:"old,omitempty"`
	New  int    `json:"new,omitempty"`
	Text string `json:"text"`
}

// DiffHunk — hunk unified diff.
type DiffHunk struct {
	Header   string     `json:"header,omitempty"`
	OldStart int        `json:"old_start"`
	OldLines int        `json:"old_lines"`
	NewStart int        `json:"new_start"`
	NewLines int        `json:"new_lines"`
	Lines    []DiffLine `json:"lines"`
}

// DiffFile — файл diff.
type DiffFile struct {
	Path    string     `json:"path"`
	OldPath string     `json:"old_path,omitempty"`
	Status  string     `json:"status"`
	Binary  bool       `json:"binary,omitempty"`
	Hunks   []DiffHunk `json:"hunks"`
	Added   int        `json:"added"`
	Removed int        `json:"removed"`
}

// ReviewInput — что ревьюировали: заголовок, диапазон или номер PR, файлы.
type ReviewInput struct {
	Title     string     `json:"title"`
	Body      string     `json:"body,omitempty"`
	Base      string     `json:"base,omitempty"`
	Head      string     `json:"head,omitempty"`
	Range     string     `json:"range,omitempty"`
	Number    int        `json:"number,omitempty"`
	Files     []DiffFile `json:"files"`
	Truncated bool       `json:"truncated,omitempty"`
}

// ReviewEvidence — основание находки: фрагмент документации с дословной
// цитатой (rule, docs) или строка прочитанного файла (bug). Title и
// Section — заголовок фрагмента по выдаче хода или базе; их заполняет окно.
type ReviewEvidence struct {
	ChunkID string `json:"chunk_id,omitempty"`
	Quote   string `json:"quote,omitempty"`
	Path    string `json:"path,omitempty"`
	Line    int    `json:"line,omitempty"`
	Title   string `json:"title,omitempty"`
	Section string `json:"section,omitempty"`
}

// ReviewFinding — находка: файл и строка diff (новая нумерация), вид
// bug|rule|docs|recommendation, важность major|minor.
type ReviewFinding struct {
	File     string         `json:"file"`
	Line     int            `json:"line"`
	Kind     string         `json:"kind"`
	Severity string         `json:"severity"`
	Text     string         `json:"text"`
	Evidence ReviewEvidence `json:"evidence"`
}

// ReviewTurn — итог хода ревью, как его видит окно.
type ReviewTurn struct {
	Input      ReviewInput
	Findings   []ReviewFinding
	Summary    string
	Verdict    string
	Markdown   string
	Check      cite.CiteCheck
	Rejected   int
	Unverified bool
	Stats      agent.Stats
	Calls      []help.Call
	Hits       []kb.Hit
	Features   features.Set
}

// PlantedRow — строка списка PR: подставной из eval/review/prs.json или
// «текущая ветка» (Range не пуст).
type PlantedRow struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind,omitempty"`
	Clean bool   `json:"clean"`
	File  string `json:"file,omitempty"`
	Line  int    `json:"line,omitempty"`
	Note  string `json:"note,omitempty"`
	Range string `json:"range,omitempty"`
}

// BranchID — id пункта «текущая ветка» в списке PR.
const BranchID = "branch"

// PlantedResult — нашёл ли агент посаженную ошибку: Line — посаженная
// строка, Finding — находка, которая её закрыла. Для чистого PR (Clean)
// Found — «ложных находок нет».
type PlantedResult struct {
	ID      string         `json:"id"`
	Clean   bool           `json:"clean,omitempty"`
	Found   bool           `json:"found"`
	File    string         `json:"file,omitempty"`
	Line    int            `json:"line,omitempty"`
	Kind    string         `json:"kind,omitempty"`
	Finding *ReviewFinding `json:"finding,omitempty"`
}

// ReviewView — ответ окну (event: answer).
type ReviewView struct {
	Input      ReviewInput     `json:"input"`
	Findings   []ReviewFinding `json:"findings"`
	Verdict    string          `json:"verdict"`
	Summary    string          `json:"summary"`
	Markdown   string          `json:"markdown"`
	Check      cite.CiteCheck  `json:"check"`
	Stats      agent.Stats     `json:"stats"`
	Planted    *PlantedResult  `json:"planted,omitempty"`
	Calls      []help.Call     `json:"calls,omitempty"`
	Unverified bool            `json:"unverified"`
	Features   features.Set    `json:"features"`
	Error      string          `json:"error,omitempty"`
	Seconds    float64         `json:"seconds"`
}

// CIRun — панель «последний прогон Action»: разбор examples/review/ci-*.json
// по тому, что в нём нашлось (объект с number/verdict/url/at или журнал
// gh api — массив ревью GitHub).
type CIRun struct {
	Number   int    `json:"number,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	URL      string `json:"url,omitempty"`
	At       string `json:"at,omitempty"`
	Title    string `json:"title,omitempty"`
	Findings int    `json:"findings,omitempty"`
	File     string `json:"file"`
}

// Review — вкладка «Ревью». nil — её маршруты отвечают 503 (панель
// Action — 404).
type Review struct {
	// Planted — подставные PR стенда; nil — в списке только «текущая ветка».
	Planted func() ([]PlantedRow, error)
	// Range — диапазон «текущей ветки»; пусто — DefaultRange.
	Range string
	// Run — ход ревью; события — в em. nil — модель не настроена (503).
	Run func(ctx context.Context, req ReviewRequest, em agent.Emitter) (*ReviewTurn, error)
	// ExamplesDir — каталог с ci-*.json; пусто или нет файлов — 404.
	ExamplesDir string
}

// reviewRoutes — маршруты вкладки «Ревью».
func (s *Server) reviewRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/review/planted", s.reviewPlanted)
	mux.HandleFunc("POST /api/review/run", s.reviewRun)
	mux.HandleFunc("GET /api/review/ci", s.reviewCI)
}

func (s *Server) review(w http.ResponseWriter) (*Review, bool) {
	if s.Review == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ревью не настроено: curator serve -planted <каталог>"})
		return nil, false
	}
	return s.Review, true
}

func (r *Review) rangeOr(rng string) string {
	if v := strings.TrimSpace(rng); v != "" {
		return v
	}
	if v := strings.TrimSpace(r.Range); v != "" {
		return v
	}
	return DefaultRange
}

// rows — подставные PR плюс «текущая ветка» последним пунктом.
func (r *Review) rows() ([]PlantedRow, error) {
	rows := []PlantedRow{}
	var err error
	if r.Planted != nil {
		list, e := r.Planted()
		if e != nil {
			err = e
		}
		rows = append(rows, list...)
	}
	rows = append(rows, PlantedRow{ID: BranchID, Title: "текущая ветка", Range: r.rangeOr("")})
	return rows, err
}

func (s *Server) reviewPlanted(w http.ResponseWriter, r *http.Request) {
	rv, ok := s.review(w)
	if !ok {
		return
	}
	rows, err := rv.rows()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "подставные PR: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, rows)
}

// reviewRun — «Ревью»: SSE как /api/ask — event: journal (agent.Event),
// event: answer (ReviewView), event: error.
func (s *Server) reviewRun(w http.ResponseWriter, r *http.Request) {
	rv, ok := s.review(w)
	if !ok {
		return
	}
	var req ReviewRequest
	if r.Body != nil {
		if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело запроса: " + err.Error()})
			return
		}
	}
	req.Planted = strings.TrimSpace(req.Planted)
	if req.Planted == BranchID {
		req.Planted = ""
	}
	var planted *PlantedRow
	if req.Planted != "" {
		rows, err := rv.rows()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "подставные PR: " + err.Error()})
			return
		}
		for i := range rows {
			if rows[i].ID == req.Planted && rows[i].Range == "" {
				planted = &rows[i]
				break
			}
		}
		if planted == nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "подставного PR нет: " + req.Planted})
			return
		}
		req.Range = ""
	} else {
		req.Range = rv.rangeOr(req.Range)
	}
	if rv.Run == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "модель не настроена: нужен DEEPSEEK_API_KEY или -llm local"})
		return
	}
	em, ok := openSSE(w)
	if !ok {
		return
	}
	started := time.Now()
	turn, err := rv.Run(r.Context(), req, em)
	if turn == nil {
		if err == nil {
			err = errors.New("ход не дал ответа")
		}
		em.send("error", map[string]string{"error": err.Error()})
		return
	}
	view := s.reviewView(r.Context(), turn, planted)
	view.Seconds = time.Since(started).Seconds()
	if err != nil {
		view.Error = err.Error()
	}
	em.send("answer", view)
}

// reviewView — ответ окну: вердикт (пустой — по находкам), заголовки
// фрагментов у оснований по выдаче хода, а чего в выдаче нет — по базе,
// итог по посаженной ошибке.
func (s *Server) reviewView(ctx context.Context, t *ReviewTurn, planted *PlantedRow) ReviewView {
	v := ReviewView{Input: t.Input, Findings: append([]ReviewFinding{}, t.Findings...), Verdict: t.Verdict, Summary: t.Summary, Markdown: t.Markdown,
		Check: t.Check, Stats: t.Stats, Unverified: t.Unverified || t.Check.Unverified, Features: t.Features}
	if v.Input.Files == nil {
		v.Input.Files = []DiffFile{}
	}
	if v.Verdict == "" {
		v.Verdict = "changes"
		if len(v.Findings) == 0 {
			v.Verdict = "ok"
		}
	}
	for _, c := range t.Calls {
		c.Result = tools.Truncate(c.Result, 2000)
		v.Calls = append(v.Calls, c)
	}
	byID := map[string]kb.Chunk{}
	for _, h := range t.Hits {
		byID[h.ID] = h.Chunk
	}
	for i := range v.Findings {
		ev := &v.Findings[i].Evidence
		id := strings.TrimSpace(ev.ChunkID)
		if id == "" || ev.Title != "" {
			continue
		}
		c, ok := byID[id]
		if !ok && s.Deps.Store != nil {
			if got, err := s.Deps.Store.Chunk(ctx, id); err == nil {
				c, ok = got, true
			}
		}
		if ok {
			ev.Title, ev.Section = c.Title, pathOf(c)
		}
	}
	if planted != nil {
		v.Planted = plantedResult(*planted, v.Findings)
	}
	return v
}

// plantedResult — найдена ли посаженная ошибка: тот же файл,
// |строка − посаженная| ≤ PlantedTolerance, ожидаемый вид (если задан);
// из нескольких — ближайшая. Чистый PR «найден», если находок нет.
func plantedResult(p PlantedRow, findings []ReviewFinding) *PlantedResult {
	res := &PlantedResult{ID: p.ID, Clean: p.Clean, File: p.File, Line: p.Line, Kind: p.Kind}
	if p.Clean {
		res.Found = len(findings) == 0
		return res
	}
	best := -1
	for i, f := range findings {
		if samePath(f.File, p.File) || p.File == "" {
			d := f.Line - p.Line
			if d < 0 {
				d = -d
			}
			if d > PlantedTolerance {
				continue
			}
			if p.Kind != "" && !strings.EqualFold(strings.TrimSpace(f.Kind), p.Kind) {
				continue
			}
			if best < 0 || d < absInt(findings[best].Line-p.Line) {
				best = i
			}
		}
	}
	if best >= 0 {
		f := findings[best]
		res.Found, res.Finding = true, &f
	}
	return res
}

func absInt(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// samePath — пути равны с точностью до разделителей и «./».
func samePath(a, b string) bool {
	norm := func(s string) string {
		s = strings.TrimSpace(filepath.ToSlash(s))
		s = strings.TrimPrefix(s, "./")
		return strings.TrimPrefix(s, "/")
	}
	return norm(a) == norm(b)
}

// reviewCI — последний examples/review/ci-*.json; нет каталога или
// файлов — 404 (панель скрыта).
func (s *Server) reviewCI(w http.ResponseWriter, r *http.Request) {
	dir := ""
	if s.Review != nil {
		dir = s.Review.ExamplesDir
	}
	run, ok := LastCI(dir)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "прогонов Action нет: examples/review/ci-*.json"})
		return
	}
	writeJSON(w, http.StatusOK, run)
}

var ciNum = regexp.MustCompile(`^ci-(\d+)`)

// LastCI — последний по номеру (иначе по имени) ci-*.json каталога.
func LastCI(dir string) (CIRun, bool) {
	if strings.TrimSpace(dir) == "" {
		return CIRun{}, false
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "ci-*.json"))
	if len(matches) == 0 {
		return CIRun{}, false
	}
	key := func(p string) (int, string) {
		base := filepath.Base(p)
		if m := ciNum.FindStringSubmatch(base); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n, base
		}
		return -1, base
	}
	sort.Slice(matches, func(i, j int) bool {
		ni, bi := key(matches[i])
		nj, bj := key(matches[j])
		if ni != nj {
			return ni < nj
		}
		return bi < bj
	})
	last := matches[len(matches)-1]
	b, err := os.ReadFile(last)
	if err != nil {
		return CIRun{}, false
	}
	run := ParseCI(filepath.Base(last), b)
	return run, true
}

// ParseCI — разбор журнала прогона Action «как получится»: объект с
// полями number/pr, verdict, url/html_url, at/time/submitted_at, title,
// findings (список или число) либо массив ревью GitHub (gh api
// …/pulls/N/reviews): берётся последнее ревью с маркером curator-review,
// вердикт — по тексту («замечаний нет» → ok, иначе changes). Номер PR —
// из файла ci-<n>.json, если в данных его нет.
func ParseCI(name string, b []byte) CIRun {
	run := CIRun{File: name}
	if m := ciNum.FindStringSubmatch(name); m != nil {
		run.Number, _ = strconv.Atoi(m[1])
	}
	var raw interface{}
	if err := json.Unmarshal(b, &raw); err != nil {
		return run
	}
	switch v := raw.(type) {
	case map[string]interface{}:
		fillCI(&run, v)
		if list, ok := v["reviews"].([]interface{}); ok {
			fillCIReviews(&run, list)
		} else if rev, ok := v["review"].(map[string]interface{}); ok {
			fillCIReview(&run, rev)
		}
	case []interface{}:
		fillCIReviews(&run, v)
	}
	return run
}

func fillCI(run *CIRun, m map[string]interface{}) {
	if n := ciInt(m, "number", "pr", "pr_number", "pull"); n > 0 {
		run.Number = n
	}
	if s := ciStr(m, "verdict"); s != "" {
		run.Verdict = s
	}
	if s := ciStr(m, "url", "html_url", "link"); s != "" {
		run.URL = s
	}
	if s := ciStr(m, "at", "time", "submitted_at", "created_at", "when"); s != "" {
		run.At = s
	}
	if s := ciStr(m, "title"); s != "" {
		run.Title = s
	}
	switch f := m["findings"].(type) {
	case []interface{}:
		run.Findings = len(f)
	case float64:
		run.Findings = int(f)
	}
	if run.Verdict == "" {
		run.Verdict = verdictOfText(ciStr(m, "body", "markdown", "text", "summary"))
	}
}

func fillCIReviews(run *CIRun, list []interface{}) {
	var pick map[string]interface{}
	for _, it := range list {
		m, ok := it.(map[string]interface{})
		if !ok {
			continue
		}
		if pick == nil || strings.Contains(ciStr(m, "body"), "curator-review") {
			pick = m
		}
	}
	if pick != nil {
		fillCIReview(run, pick)
	}
}

func fillCIReview(run *CIRun, m map[string]interface{}) {
	if s := ciStr(m, "html_url", "url"); s != "" {
		run.URL = s
	}
	if s := ciStr(m, "submitted_at", "created_at", "at"); s != "" {
		run.At = s
	}
	if run.Number == 0 {
		if u := ciStr(m, "pull_request_url"); u != "" {
			if i := strings.LastIndex(u, "/"); i >= 0 {
				run.Number, _ = strconv.Atoi(u[i+1:])
			}
		}
	}
	if s := ciStr(m, "verdict"); s != "" {
		run.Verdict = s
	} else if run.Verdict == "" {
		run.Verdict = verdictOfText(ciStr(m, "body"))
	}
}

// verdictOfText — вердикт по тексту ревью; пусто — не понять.
func verdictOfText(s string) string {
	l := strings.ToLower(s)
	switch {
	case l == "":
		return ""
	case strings.Contains(l, "замечаний нет"):
		return "ok"
	case strings.Contains(l, "нужны правки") || strings.Contains(l, "changes"):
		return "changes"
	}
	return ""
}

func ciStr(m map[string]interface{}, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func ciInt(m map[string]interface{}, keys ...string) int {
	for _, k := range keys {
		switch v := m[k].(type) {
		case float64:
			return int(v)
		case string:
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
				return n
			}
		}
	}
	return 0
}
