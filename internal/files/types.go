// Package files — файловый агент на правах этапа (день 34, И-17): на
// этапе plan у модели только чтение и поиск, инструменты записи
// появляются после согласия человека и действуют только на файлы из
// плана, только в worktree `sandbox/agent/<run>` на ветке `agent/<run>`;
// слияние — человек.
//
// Этот файл — типы контракта, общие для инструментов записи (tools.go,
// writelog.go), сценариев (script.go), этапов (agent.go, session.go),
// стенда (bench/t17) и окна (web/files). Имена — по брифу дня 34,
// менять нельзя.
package files

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/help"
)

// Stage — этап прогона.
type Stage string

// Этапы: план (только чтение) → согласие → применение (запись в
// worktree) → готово.
const (
	StagePlan    Stage = "plan"
	StageApprove Stage = "approve"
	StageApply   Stage = "apply"
	StageDone    Stage = "done"
)

// Имена финишёров этапов.
const (
	PlanFinish = "plan_done"
	DoneFinish = "files_done"
)

// Имена инструментов записи (выдаются только на этапе apply).
const (
	WriteFile  = "write_file"
	ApplyPatch = "apply_patch"
	// ReplaceInFile — точная замена подстроки: надёжнее diff для модели.
	ReplaceInFile = "replace_in_file"
	GitCommit     = "git_commit"
)

// WriteTools — имена инструментов записи по порядку.
var WriteTools = []string{WriteFile, ReplaceInFile, ApplyPatch, GitCommit}

// DefaultAllowed — каталоги корня курса, куда агенту можно писать
// (префиксы путей с «/»).
var DefaultAllowed = []string{"docs/", "demos/", "templates/", ".claude/skills/", "tools/curator/"}

// DefaultDeny — что писать нельзя даже внутри разрешённых каталогов:
// служебный git, ключи, сгенерированные INDEX.md и docs/registry.json,
// задания (их в worktree нет) и сама песочница. Синтаксис — как у
// gitx.Guard: образец без «/» — любой элемент пути, с «/» — путь целиком.
var DefaultDeny = []string{".git", ".env", ".env.*", "*.local", "INDEX.md", "docs/registry.json", "assignments", "sandbox"}

// PlanFile — файл плана: путь от корня с «/», New — файла ещё нет
// (будет создан), Why — зачем он нужен.
type PlanFile struct {
	Path string `json:"path"`
	New  bool   `json:"new,omitempty"`
	Why  string `json:"why"`
}

// Plan — аргументы финишёра plan_done: цель своими словами, файлы,
// шаги и нужна ли запись. Writes=false — все файлы только для чтения,
// новых среди них быть не может.
type Plan struct {
	Goal   string     `json:"goal"`
	Files  []PlanFile `json:"files"`
	Steps  []string   `json:"steps"`
	Writes bool       `json:"writes"`
}

// Done — аргументы финишёра files_done: какие файлы изменены (сверяется
// с git status worktree — считает код) и итог для человека.
type Done struct {
	Changed []string `json:"changed"`
	Summary string   `json:"summary"`
}

// Options — настройки прогона: механизмы (files.gates и др.), модель,
// Yes — согласие без вопроса (-yes), Allowed/Deny — ограда записи
// (пустые — DefaultAllowed/DefaultDeny), MaxSteps — предел шагов этапа.
type Options struct {
	Features features.Set
	Model    string
	Yes      bool
	Allowed  []string
	Deny     []string
	MaxSteps int
	// Acceptance — приёмочные проверки этапа apply (как тесты для работы
	// агента): финишёр files_done прогоняет каждую на worktree, неудача —
	// отказ с объяснением; FilesMin — затронуто файлов не меньше (0 —
	// правило плана: min(2, файлов плана)). RunScript берёт их из Expect.
	Acceptance []Check
	FilesMin   int
}

// AllowedOr — разрешённые каталоги или умолчания.
func (o Options) AllowedOr() []string {
	if len(o.Allowed) == 0 {
		return DefaultAllowed
	}
	return o.Allowed
}

// DenyOr — запрещённые образцы или умолчания.
func (o Options) DenyOr() []string {
	if len(o.Deny) == 0 {
		return DefaultDeny
	}
	return o.Deny
}

// Run — прогон файлового агента: цель, этап, план и итог, корень (копия
// курса или настоящий), worktree и ветка, изменённые файлы и diff
// (считает код по git), коммит, счётчики контрольной дорожки и агента.
type Run struct {
	ID       string `json:"id"`
	Goal     string `json:"goal"`
	Stage    Stage  `json:"stage"`
	Plan     *Plan  `json:"plan,omitempty"`
	Done     *Done  `json:"done,omitempty"`
	Root     string `json:"root"`
	Worktree string `json:"worktree,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// Changed — git status --porcelain в worktree (считает код).
	Changed []string `json:"changed,omitempty"`
	// Diff — git diff HEAD (новые файлы через add -N) — считает код.
	Diff string `json:"diff,omitempty"`
	// Commit — sha коммита в worktree, если был.
	Commit string `json:"commit,omitempty"`
	// WritesBeforeApprove — вызовы инструментов записи до согласия
	// (контрольная дорожка без прав этапа); WritesOutsidePlan — попытки
	// записи вне плана (отказаны инструментом).
	WritesBeforeApprove int         `json:"writes_before_approve"`
	WritesOutsidePlan   int         `json:"writes_outside_plan"`
	Rejected            int         `json:"rejected"`
	Unverified          bool        `json:"unverified"`
	Stats               agent.Stats `json:"stats"`
	Calls               []help.Call `json:"calls"`
	Seconds             float64     `json:"seconds"`
}

// Виды проверок сценария (Check.Kind).
const (
	CheckContains       = "contains"        // Path содержит Text (или Pattern)
	CheckAbsent         = "absent"          // Path не содержит Text (или Pattern); файла нет — отказ
	CheckExists         = "exists"          // Path существует
	CheckRegex          = "regex"           // Path соответствует Pattern
	CheckDiffWithin     = "diff_within"     // изменения Path только в разделе Text (заголовок) или строках Range
	CheckQuotesVerbatim = "quotes_verbatim" // цитаты «…» из Path — подстроки файлов Source
	CheckCoverage       = "coverage"        // доля файлов Source с Pattern, упомянутых в Text (или Path) ≥ Min
)

// CheckKinds — виды проверок по порядку.
var CheckKinds = []string{CheckContains, CheckAbsent, CheckExists, CheckRegex, CheckDiffWithin, CheckQuotesVerbatim, CheckCoverage}

// Check — проверка результата сценария на worktree. Поля по виду:
// Path — файл от корня; Text — подстрока (contains/absent), заголовок
// раздела (diff_within) или проверяемый текст (coverage: пусто —
// подставляется summary прогона); Pattern — регулярное выражение RE2;
// Range — явный диапазон строк [от, до] для diff_within; Source —
// образцы файлов-источников через запятую (quotes_verbatim, coverage);
// Min — порог доли (coverage, quotes_verbatim; 0 — 1.0).
type Check struct {
	Kind    string  `json:"kind"`
	Path    string  `json:"path,omitempty"`
	Text    string  `json:"text,omitempty"`
	Pattern string  `json:"pattern,omitempty"`
	Range   [2]int  `json:"range,omitempty"`
	Source  string  `json:"source,omitempty"`
	Min     float64 `json:"min,omitempty"`
}

// Expect — ожидания сценария: FilesMin — затронуто файлов не меньше,
// FilesAllowed — образцы путей (префикс с «/» или glob), куда можно
// писать (пусто — любые разрешённые), Writes — план должен быть с
// записью (или без), Checks — проверки на worktree.
type Expect struct {
	FilesMin     int      `json:"files_min"`
	FilesAllowed []string `json:"files_allowed,omitempty"`
	Writes       bool     `json:"writes"`
	Checks       []Check  `json:"checks"`
}

// Script — воспроизводимый сценарий eval/files/*.json: цель словами и
// ожидания стенда.
type Script struct {
	ID     string `json:"id"`
	Goal   string `json:"goal"`
	Expect Expect `json:"expect"`
}

// CheckResult — исход проверки: сама проверка, пройдена ли и что
// получилось словами (для отчёта).
type CheckResult struct {
	Check Check  `json:"check"`
	OK    bool   `json:"ok"`
	Got   string `json:"got"`
}

// NewID — идентификатор прогона: время и 8 hex случайных символов
// (`20261017-153012-9f2c1a7b`); он же — имя worktree и суффикс ветки.
func NewID() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return time.Now().Format("20060102-150405") + "-" + hex.EncodeToString(b[:])
}

// NormPath — путь от корня с «/»: без «./», обратных слэшей, лишних
// пробелов, ведущего и завершающего «/». «..» не схлопывается в корень:
// «docs/../../x» остаётся «../x», и ограда его отклонит.
func NormPath(p string) string {
	p = strings.TrimSpace(filepath.ToSlash(p))
	p = strings.TrimPrefix(p, "./")
	if p == "" {
		return ""
	}
	p = strings.TrimPrefix(path.Clean(p), "/")
	if p == "." {
		return ""
	}
	return p
}

// Has — есть ли путь в плане (после NormPath).
func (p *Plan) Has(rel string) (PlanFile, bool) {
	if p == nil {
		return PlanFile{}, false
	}
	rel = NormPath(rel)
	for _, f := range p.Files {
		if NormPath(f.Path) == rel {
			return f, true
		}
	}
	return PlanFile{}, false
}

// Paths — пути плана по порядку (после NormPath).
func (p *Plan) Paths() []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Files))
	for _, f := range p.Files {
		out = append(out, NormPath(f.Path))
	}
	return out
}

// Allowed — лежит ли путь в одном из разрешённых каталогов (префикс с
// «/») или совпадает с образцом path.Match.
func Allowed(rel string, allowed []string) bool {
	rel = NormPath(rel)
	for _, a := range allowed {
		a = strings.TrimSpace(filepath.ToSlash(a))
		if a == "" {
			continue
		}
		if strings.HasSuffix(a, "/") {
			if strings.HasPrefix(rel, a) {
				return true
			}
			continue
		}
		if rel == a || strings.HasPrefix(rel, a+"/") {
			return true
		}
		if ok, _ := path.Match(a, rel); ok {
			return true
		}
	}
	return false
}

// ErrPlan — план не прошёл проверку кодом.
var ErrPlan = errors.New("план отклонён")

// Validate — проверки плана кодом (финишёр plan_done): есть хотя бы один
// файл; каждый путь существует в root или помечен new; пути внутри
// Allowed и не под Deny; writes=false ⇒ новых файлов нет. Пути плана
// приводятся к NormPath. Ошибка — словами для модели, чтобы она
// перепланировала.
func (p *Plan) Validate(root string, o Options) error {
	if p == nil || len(p.Files) == 0 {
		return fmt.Errorf("%w: в плане нет ни одного файла", ErrPlan)
	}
	guard := gitx.Guard{Root: root, Deny: o.DenyOr()}
	seen := map[string]bool{}
	for i := range p.Files {
		f := &p.Files[i]
		f.Path = NormPath(f.Path)
		if f.Path == "" {
			return fmt.Errorf("%w: пустой путь в files", ErrPlan)
		}
		if seen[f.Path] {
			return fmt.Errorf("%w: %s повторяется", ErrPlan, f.Path)
		}
		seen[f.Path] = true
		if !Allowed(f.Path, o.AllowedOr()) {
			return fmt.Errorf("%w: %s вне разрешённых каталогов (%s)", ErrPlan, f.Path, strings.Join(o.AllowedOr(), ", "))
		}
		abs, err := guard.Resolve(f.Path)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrPlan, err)
		}
		st, err := os.Stat(abs)
		switch {
		case err == nil && st.IsDir():
			return fmt.Errorf("%w: %s — каталог, нужен файл", ErrPlan, f.Path)
		case err == nil && f.New:
			return fmt.Errorf("%w: %s уже есть, а помечен new", ErrPlan, f.Path)
		case err != nil && !f.New:
			return fmt.Errorf("%w: файла %s нет — пометь new или убери из плана", ErrPlan, f.Path)
		case f.New && !p.Writes:
			return fmt.Errorf("%w: %s помечен new при writes=false", ErrPlan, f.Path)
		}
	}
	return nil
}
