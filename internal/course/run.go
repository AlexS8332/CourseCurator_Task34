package course

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// RunReport — итог контрольного прогона задания из
// demos/NN-slug/runs/<время>/report.json (пишет tools/edgerec, структура —
// edgerec/report.go). Здесь только то, что нужно ассистенту: прошёл ли,
// сколько проверок, длина видео, шаги и ошибки JS.
type RunReport struct {
	Dir          string  `json:"dir"`
	Title        string  `json:"title,omitempty"`
	Started      string  `json:"started,omitempty"`
	Passed       bool    `json:"passed"`
	Checks       int     `json:"checks"`
	ChecksOK     int     `json:"checks_ok"`
	Frames       int     `json:"frames,omitempty"`
	VideoSeconds float64 `json:"video_seconds,omitempty"`
	Video        string  `json:"video,omitempty"`
	Steps        []Step  `json:"steps"`
	// Failed — первый упавший шаг (nil — все прошли).
	Failed   *Step    `json:"failed,omitempty"`
	JSErrors []string `json:"js_errors,omitempty"`
}

// Step — шаг сценария в отчёте.
type Step struct {
	N       int    `json:"n"`
	Action  string `json:"action"`
	Target  string `json:"target,omitempty"`
	Caption string `json:"caption,omitempty"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

// reportFile — report.json edgerec (только нужные поля).
type reportFile struct {
	Title        string   `json:"title"`
	Started      string   `json:"started"`
	Passed       bool     `json:"passed"`
	Checks       int      `json:"checks"`
	ChecksOK     int      `json:"checks_ok"`
	Video        string   `json:"video"`
	VideoSeconds float64  `json:"video_seconds"`
	Frames       int      `json:"frames"`
	Steps        []Step   `json:"steps"`
	JSErrors     []string `json:"js_errors"`
}

// Runs — каталоги прогонов задания по времени (имя каталога — метка
// yyyyMMdd-HHmmss, у валидации суффикс -check), старые первыми.
func (r *Root) Runs(a Assignment) ([]string, error) {
	dir := filepath.Join(r.DemoDir(a), "runs")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(out)
	return out, nil
}

// LastRun — последний прогон с записью (каталог без суффикса -check) и его
// report.json. Второе значение false — прогонов ещё не было; прогон без
// report.json (оборван) пропускается.
func (r *Root) LastRun(key string) (RunReport, bool, error) {
	a, err := r.Assignment(key)
	if err != nil {
		return RunReport{}, false, err
	}
	runs, err := r.Runs(a)
	if err != nil {
		return RunReport{}, false, err
	}
	for i := len(runs) - 1; i >= 0; i-- {
		dir := runs[i]
		if strings.HasSuffix(filepath.Base(dir), "-check") {
			continue
		}
		rep, err := ReadReport(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return RunReport{}, false, err
		}
		return rep, true, nil
	}
	return RunReport{}, false, nil
}

// ReadReport читает report.json из каталога прогона.
func ReadReport(dir string) (RunReport, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "report.json"))
	if err != nil {
		return RunReport{}, err
	}
	var f reportFile
	if err := json.Unmarshal(stripBOM(raw), &f); err != nil {
		return RunReport{}, fmt.Errorf("%s/report.json: %w", dir, err)
	}
	rep := RunReport{Dir: dir, Title: f.Title, Started: f.Started, Passed: f.Passed, Checks: f.Checks,
		ChecksOK: f.ChecksOK, Frames: f.Frames, VideoSeconds: f.VideoSeconds, Video: f.Video,
		Steps: f.Steps, JSErrors: f.JSErrors}
	if rep.Steps == nil {
		rep.Steps = []Step{}
	}
	for i := range f.Steps {
		if f.Steps[i].Status == "fail" {
			s := f.Steps[i]
			rep.Failed = &s
			break
		}
	}
	return rep, nil
}

// Scenario — сценарий контрольного прогона (demos/NN-slug/scenario.json)
// в двух словах: заголовок, подзаголовок, число шагов.
type Scenario struct {
	Path     string `json:"path"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle,omitempty"`
	Steps    int    `json:"steps"`
}

// Scenario читает сценарий задания; false — сценария нет.
func (r *Root) Scenario(a Assignment) (Scenario, bool, error) {
	path := filepath.Join(r.DemoDir(a), "scenario.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Scenario{}, false, nil
	}
	if err != nil {
		return Scenario{}, false, err
	}
	var f struct {
		Title    string            `json:"title"`
		Subtitle string            `json:"subtitle"`
		Steps    []json.RawMessage `json:"steps"`
	}
	if err := json.Unmarshal(stripBOM(raw), &f); err != nil {
		return Scenario{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return Scenario{Path: path, Title: f.Title, Subtitle: f.Subtitle, Steps: len(f.Steps)}, true, nil
}
