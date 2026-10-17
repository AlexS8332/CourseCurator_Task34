package web

// Вкладка «Файлы» (день 34): цель словами → план (этап plan: у агента
// только чтение и поиск) → согласие человека репликой → правки в worktree
// (этап apply: write_file, apply_patch, git_commit — только файлы плана)
// → отчёт: ветка, изменённые файлы, diff, коммит, счётчики.
//
// Окно не знает устройства пакета files: этапы приходят замыканиями
// (serve.go подставляет files.New/DoPlan/Approve/DoApply, тесты —
// подставки), прогон — своим типом окна, diff — теми же DiffFile, что у
// вкладки «Ревью», чтобы страница рисовала его одним кодом. Состояние
// прогона между планом и согласием живёт на сервере по run_id.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/tools"
)

// Этапы прогона, как их называет контракт пакета files.
const (
	FilesStagePlan    = "plan"
	FilesStageApprove = "approve"
	FilesStageApply   = "apply"
	FilesStageDone    = "done"
)

// WriteAfterConsent — слова строки прав, пока инструментов записи нет.
const WriteAfterConsent = "после согласия"

// FilesWriteTools — инструменты записи этапа apply по контракту.
var FilesWriteTools = []string{"write_file", "apply_patch", "git_commit"}

// FilesPlanFile — файл плана: путь, новый ли, зачем.
type FilesPlanFile struct {
	Path string `json:"path"`
	New  bool   `json:"new,omitempty"`
	Why  string `json:"why,omitempty"`
}

// FilesPlan — план агента: файлы, шаги, будут ли правки.
type FilesPlan struct {
	Goal   string          `json:"goal,omitempty"`
	Files  []FilesPlanFile `json:"files"`
	Steps  []string        `json:"steps"`
	Writes bool            `json:"writes"`
}

// FilesRights — права этапа: инструменты чтения и записи. В JSON write —
// список, а пока записи нет (этап plan при включённых правах) — строка
// «после согласия».
type FilesRights struct {
	Read  []string
	Write []string
	// Gated — запись появится после согласия (её ещё нет).
	Gated bool
}

func (r FilesRights) MarshalJSON() ([]byte, error) {
	out := struct {
		Read  []string `json:"read"`
		Write any      `json:"write"`
	}{Read: r.Read, Write: r.Write}
	if out.Read == nil {
		out.Read = []string{}
	}
	if r.Write == nil {
		if r.Gated {
			out.Write = WriteAfterConsent
		} else {
			out.Write = []string{}
		}
	}
	return json.Marshal(out)
}

func (r *FilesRights) UnmarshalJSON(b []byte) error {
	var in struct {
		Read  []string        `json:"read"`
		Write json.RawMessage `json:"write"`
	}
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*r = FilesRights{Read: in.Read}
	if len(in.Write) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(in.Write, &s); err == nil {
		r.Gated = true
		return nil
	}
	return json.Unmarshal(in.Write, &r.Write)
}

// FilesCounters — контрольные счётчики прогона: вызовы записи до согласия
// (на основной дорожке — 0: инструментов нет), попытки записи вне плана
// (отказаны инструментом), отказы проверки финишёра.
type FilesCounters struct {
	WritesBeforeApprove int `json:"writes_before_approve"`
	WritesOutsidePlan   int `json:"writes_outside_plan"`
	Rejected            int `json:"rejected,omitempty"`
}

// FilesRun — прогон файлового агента, как его видит окно.
type FilesRun struct {
	ID       string        `json:"run_id"`
	Goal     string        `json:"goal"`
	Stage    string        `json:"stage"`
	Plan     *FilesPlan    `json:"plan,omitempty"`
	Branch   string        `json:"branch,omitempty"`
	Worktree string        `json:"worktree,omitempty"`
	Changed  []string      `json:"changed"`
	Diff     []DiffFile    `json:"diff"`
	Commit   string        `json:"commit,omitempty"`
	Summary  string        `json:"summary,omitempty"`
	Markdown string        `json:"markdown,omitempty"`
	Counters FilesCounters `json:"counters"`
	Stats    agent.Stats   `json:"stats"`
	Calls    []help.Call   `json:"calls,omitempty"`
	// Unverified — результат не прошёл проверку кодом (финишёр отказан до
	// предела шагов).
	Unverified bool    `json:"unverified"`
	Seconds    float64 `json:"seconds"`
}

// FilesView — ответ окну: event: plan (после этапа plan и при отказе
// согласия — с Note) или event: report (после apply).
type FilesView struct {
	View string `json:"view"`
	FilesRun
	Rights   FilesRights  `json:"rights"`
	Note     string       `json:"note,omitempty"`
	Features features.Set `json:"features"`
	Error    string       `json:"error,omitempty"`
}

// FilesRow — строка списка прогонов сессии.
type FilesRow struct {
	ID      string  `json:"run_id"`
	Goal    string  `json:"goal"`
	Stage   string  `json:"stage"`
	Branch  string  `json:"branch,omitempty"`
	Changed int     `json:"changed"`
	Commit  string  `json:"commit,omitempty"`
	Seconds float64 `json:"seconds"`
}

// Files — вкладка «Файлы». nil — её маршруты отвечают 503.
type Files struct {
	// Plan — новый прогон по цели и этап plan; события — в em. nil —
	// модель не настроена (503).
	Plan func(ctx context.Context, goal string, em agent.Emitter) (*FilesRun, error)
	// Approve — реплика человека по прогону: согласие есть — этап apply
	// (правки в worktree) и прогон после него, approved=true; согласия нет
	// — прогон без изменений, approved=false. Прогона нет — ошибка.
	Approve func(ctx context.Context, runID, reply string, em agent.Emitter) (run *FilesRun, approved bool, err error)
	// Runs — прогоны сессии, свежие первыми; nil — список пуст.
	Runs func() []FilesRun
	// Gated — права этапа включены (features.FilesGates): инструменты
	// записи появляются после согласия. Выключены — выданы с первого шага.
	Gated bool
	// ReadTools — инструменты чтения этапа plan для строки прав; пусто —
	// роль files из project.Roles.
	ReadTools []string
	// WriteTools — инструменты записи этапа apply; пусто — FilesWriteTools.
	WriteTools []string
	// Features — набор механизмов прогона (в ответ окну).
	Features features.Set
}

// filesRoutes — маршруты вкладки «Файлы».
func (s *Server) filesRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/files/plan", s.filesPlan)
	mux.HandleFunc("POST /api/files/approve", s.filesApprove)
	mux.HandleFunc("GET /api/files/runs", s.filesRuns)
}

func (s *Server) files(w http.ResponseWriter) (*Files, bool) {
	if s.Files == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "файловый агент не настроен: curator serve -root <корень>"})
		return nil, false
	}
	return s.Files, true
}

// Rights — права этапа: чтение всегда, запись — по этапу и механизму.
func (f *Files) Rights(stage string) FilesRights {
	r := FilesRights{Read: append([]string{}, f.readTools()...)}
	write := f.WriteTools
	if len(write) == 0 {
		write = FilesWriteTools
	}
	switch {
	case !f.Gated, stage == FilesStageApply, stage == FilesStageDone:
		r.Write = append([]string{}, write...)
	default:
		r.Gated = true
	}
	return r
}

func (f *Files) readTools() []string {
	if len(f.ReadTools) > 0 {
		return f.ReadTools
	}
	return project.Roles["files"]
}

// view — ответ окну из прогона: diff и список файлов не nil, журнал
// вызовов обрезан, права — по этапу.
func (f *Files) view(kind string, r *FilesRun, started time.Time, err error) FilesView {
	v := FilesView{View: kind, FilesRun: *r, Rights: f.Rights(r.Stage), Features: f.Features}
	if v.Changed == nil {
		v.Changed = []string{}
	}
	if v.Diff == nil {
		v.Diff = []DiffFile{}
	}
	v.Calls = nil
	for _, c := range r.Calls {
		c.Result = tools.Truncate(c.Result, 2000)
		v.Calls = append(v.Calls, c)
	}
	if v.Seconds == 0 {
		v.Seconds = time.Since(started).Seconds()
	}
	if err != nil {
		v.Error = err.Error()
	}
	return v
}

// filesPlan — цель → SSE: event: journal (agent.Event), event: plan
// (FilesView), event: error (прогон не начался).
func (s *Server) filesPlan(w http.ResponseWriter, r *http.Request) {
	f, ok := s.files(w)
	if !ok {
		return
	}
	var in struct {
		Goal string `json:"goal"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело запроса: " + err.Error()})
		return
	}
	goal := strings.TrimSpace(in.Goal)
	if goal == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустая цель"})
		return
	}
	if f.Plan == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "модель не настроена: нужен DEEPSEEK_API_KEY или -llm local"})
		return
	}
	em, ok := openSSE(w)
	if !ok {
		return
	}
	started := time.Now()
	run, err := f.Plan(r.Context(), goal, em)
	if run == nil {
		if err == nil {
			err = errors.New("прогон не начался")
		}
		em.send("error", map[string]string{"error": err.Error()})
		return
	}
	em.send("plan", f.view("plan", run, started, err))
}

// filesApprove — реплика по прогону → SSE: event: journal, затем event:
// report (согласие есть: после apply) или event: plan (согласия нет: этап
// без изменений, Note — почему), event: error — прогон не найден.
func (s *Server) filesApprove(w http.ResponseWriter, r *http.Request) {
	f, ok := s.files(w)
	if !ok {
		return
	}
	var in struct {
		RunID string `json:"run_id"`
		Reply string `json:"reply"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxBody)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "тело запроса: " + err.Error()})
		return
	}
	in.RunID, in.Reply = strings.TrimSpace(in.RunID), strings.TrimSpace(in.Reply)
	if in.RunID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "нет run_id: сначала план"})
		return
	}
	if in.Reply == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "пустая реплика: согласие — словами («да, применяй»)"})
		return
	}
	if f.Runs != nil {
		found := false
		for _, run := range f.Runs() {
			if run.ID == in.RunID {
				found = true
				break
			}
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "прогона нет: " + in.RunID})
			return
		}
	}
	if f.Approve == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "модель не настроена: нужен DEEPSEEK_API_KEY или -llm local"})
		return
	}
	em, ok := openSSE(w)
	if !ok {
		return
	}
	started := time.Now()
	run, approved, err := f.Approve(r.Context(), in.RunID, in.Reply, em)
	if run == nil {
		if err == nil {
			err = errors.New("прогон не найден")
		}
		em.send("error", map[string]string{"error": err.Error()})
		return
	}
	if !approved {
		v := f.view("plan", run, started, err)
		v.Note = "согласия в реплике нет — этап «" + run.Stage + "» без изменений, запись не выдана: «" + in.Reply + "»"
		em.send("plan", v)
		return
	}
	em.send("report", f.view("report", run, started, err))
}

// filesRuns — прогоны сессии, свежие первыми.
func (s *Server) filesRuns(w http.ResponseWriter, r *http.Request) {
	f, ok := s.files(w)
	if !ok {
		return
	}
	rows := []FilesRow{}
	if f.Runs != nil {
		for _, run := range f.Runs() {
			rows = append(rows, FilesRow{ID: run.ID, Goal: run.Goal, Stage: run.Stage, Branch: run.Branch, Changed: len(run.Changed), Commit: run.Commit, Seconds: run.Seconds})
		}
	}
	writeJSON(w, http.StatusOK, rows)
}
