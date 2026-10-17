package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aitr/34-files-agent/internal/bench"
	"aitr/34-files-agent/internal/files"
)

func init() {
	register("files", `"цель" [-yes] | -script eval/files/F2.json -yes  [-root dir] [-data dir] [-llm cloud|local] [-features спец] [-out file.md] [-json] [-quiet]  файловый агент: план → согласие → правки в worktree`, runFiles)
}

// runFiles — `curator files "обнови таблицу команд …"`: два хода
// файлового агента. План печатается после первого хода; без -yes команда
// ждёт ответ со stdin (согласие читает consent.Decided: «да, применяй»),
// с -yes применяет сразу. Правки — в worktree sandbox/agent/<run> корня
// на ветке agent/<run>, слияние — человек. `-script F2.json` — сценарий
// eval/files с проверками на worktree (согласие автоматическое; без -yes
// команда напоминает об этом и всё равно применяет — у сценария нет
// человека). Журнал — в stderr, отчёт — в stdout markdown (или -json:
// весь Run) и в файл -out. Неприменённый план — код 0; оборванный ход или
// непройденная проверка сценария — код 1.
func runFiles(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("files", flag.ContinueOnError)
	yes := fs.Bool("yes", false, "применить план без вопроса")
	script := fs.String("script", "", "сценарий eval/files/F*.json вместо цели словами")
	out := fs.String("out", "", "записать markdown отчёт в файл")
	rootFlag := fs.String("root", "", "корень курса (по умолчанию — вверх от текущего каталога)")
	dataFlag := fs.String("data", "", "каталог данных с course.db (по умолчанию <корень>/tools/curator/data или $CURATOR_DATA)")
	llmFlag := fs.String("llm", "", "провайдер модели: cloud (DeepSeek) или local; пусто — $LLM_PROVIDER или cloud")
	featFlag := fs.String("features", "", "механизмы: «-files.gates», «all», «default»")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	asJSON := fs.Bool("json", false, "печатать итог JSON (план, изменённые файлы, diff, счётчики, вызовы)")
	quiet := fs.Bool("quiet", false, "без журнала хода в stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Флаги разрешены и после цели: `curator files "цель" -yes`.
	var goalParts []string
	rest := fs.Args()
	for len(rest) > 0 {
		arg := rest[0]
		rest = rest[1:]
		if strings.HasPrefix(arg, "-") {
			if err := fs.Parse(append([]string{arg}, rest...)); err != nil {
				return err
			}
			rest = fs.Args()
			continue
		}
		goalParts = append(goalParts, arg)
	}
	goal := strings.TrimSpace(strings.Join(goalParts, " "))
	if goal == "" && *script == "" {
		return errors.New("нужна цель словами или -script F2.json: curator files \"обнови таблицу команд в SKILL.md по scripts/aitr.ps1\"")
	}
	loadDotEnv(*rootFlag)
	cl, model, err := curatorLLM(*llmFlag)
	if err != nil {
		return err
	}
	set, err := parseFeatures(*featFlag)
	if err != nil {
		return err
	}
	d, closeDB, err := curatorDeps(ctx, *rootFlag, *dataFlag, *indexFlag)
	if err != nil {
		return err
	}
	defer closeDB()
	opts := files.Options{Model: model, Features: set, Yes: *yes}
	journal := consoleJournal{quiet: *quiet}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "модель %s · корень %s · механизмы %s\n", model, rootDir(d.Root), set)
	}

	if *script != "" {
		s, err := files.LoadScript(*script)
		if err != nil {
			return err
		}
		if !*yes && !*quiet {
			fmt.Fprintln(os.Stderr, "сценарий применяется без вопроса (согласие автоматическое) — как с -yes")
		}
		opts.Yes = true
		run, checks, runErr := files.RunScript(ctx, cl, d, s, journal, opts)
		if run == nil {
			return runErr
		}
		failed := 0
		for _, c := range checks {
			if !c.OK {
				failed++
			}
		}
		md := run.Report() + "\n" + checksMarkdown(s, checks)
		if err := emitFiles(run, md, checks, runErr, *out, *asJSON); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
		if failed > 0 {
			return fmt.Errorf("сценарий %s: не прошло проверок %d из %d", s.ID, failed, len(checks))
		}
		return nil
	}

	run, err := files.New(ctx, d, goal, opts)
	if err != nil {
		return err
	}
	if err := run.DoPlan(ctx, cl, d, journal, opts); err != nil {
		_ = emitFiles(run, run.Report(), nil, err, *out, *asJSON)
		return err
	}
	if !*asJSON {
		fmt.Println(run.Report())
	}
	approved, err := run.Approve(ctx, "", opts)
	if err != nil {
		return err
	}
	if !approved {
		fmt.Fprint(os.Stderr, "Применить план? Ответьте словами (да, применяй / нет): ")
		reply, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		approved, err = run.Approve(ctx, strings.TrimSpace(reply), opts)
		if err != nil {
			return err
		}
	}
	if !approved {
		fmt.Fprintln(os.Stderr, "план не применён: согласия в ответе нет, инструменты записи не выдавались")
		if *asJSON {
			return emitFiles(run, run.Report(), nil, nil, *out, true)
		}
		return nil
	}
	runErr := run.DoApply(ctx, cl, d, journal, opts)
	if err := emitFiles(run, run.Report(), nil, runErr, *out, *asJSON); err != nil {
		return err
	}
	return runErr
}

// emitFiles — отчёт в stdout (markdown или JSON) и в файл -out.
func emitFiles(run *files.Run, md string, checks []files.CheckResult, runErr error, out string, asJSON bool) error {
	if out != "" {
		if err := os.WriteFile(out, []byte(md), 0o644); err != nil {
			return err
		}
	}
	if asJSON {
		o := struct {
			*files.Run
			Checks   []files.CheckResult `json:"checks,omitempty"`
			Markdown string              `json:"markdown"`
			Error    string              `json:"error,omitempty"`
		}{Run: run, Checks: checks, Markdown: md}
		if runErr != nil {
			o.Error = runErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		return enc.Encode(o)
	}
	if run.Stage == files.StageDone || run.Done != nil || len(checks) > 0 {
		fmt.Println(md)
	}
	st := run.Stats
	fmt.Fprintf(os.Stderr, "\nэтап %s · файлов %d · ветка %s · запросов %d · вызовов %d · записей до согласия %d · вне плана %d · токенов %d · %s · отказов %d\n",
		run.Stage, len(run.Changed), orDash(run.Branch), st.Steps, st.ToolCalls, run.WritesBeforeApprove, run.WritesOutsidePlan, st.Usage.Total, usd(st.Cost), run.Rejected)
	return nil
}

// checksMarkdown — таблица проверок сценария.
func checksMarkdown(s files.Script, checks []files.CheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Проверки сценария %s\n\n| | Вид | Путь | Получено |\n|---|---|---|---|\n", s.ID)
	for _, c := range checks {
		mark := "✓"
		if !c.OK {
			mark = "✗"
		}
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", mark, c.Check.Kind, orDash(c.Check.Path), oneLine(c.Got, 120))
	}
	return b.String()
}

// findEvalFiles — каталог eval/files: рядом с текущим каталогом, у корня
// tools/curator или у исполняемого файла — первое, где есть сценарии.
func findEvalFiles(root string) string {
	candidates := []string{filepath.Join("eval", "files")}
	if root != "" {
		candidates = append(candidates, filepath.Join(root, "tools", "curator", "eval", "files"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "eval", "files"))
	}
	for _, c := range candidates {
		if m, _ := filepath.Glob(filepath.Join(c, "F*.json")); len(m) > 0 {
			return c
		}
	}
	return candidates[0]
}

// runBench17 — `curator bench -trial 17`: стенд И-17 на живой модели по
// набору set (eval/files), копии курса по дорожкам — во временном
// каталоге, отчёт — в out. Код 1, если хоть один порог не прошёл.
func runBench17(ctx context.Context, rootFlag, dataFlag, llmFlag, indexFlag, set, out string) error {
	loadDotEnv(rootFlag)
	cl, model, err := curatorLLM(llmFlag)
	if err != nil {
		return err
	}
	d, closeDB, err := curatorDeps(ctx, rootFlag, dataFlag, indexFlag)
	if err != nil {
		return err
	}
	defer closeDB()
	if set == "" {
		set = findEvalFiles(rootDir(d.Root))
	}
	fmt.Printf("И-17 · модель %s · набор %s · корень %s\n", model, filepath.ToSlash(set), rootDir(d.Root))
	started := time.Now()
	results, err := bench.T17(ctx, bench.T17Options{Deps: d, LLM: cl, Model: model, Set: set, Log: os.Stdout})
	if len(results) > 0 {
		name := "t17-" + started.Format("20060102-150405")
		md, js, werr := bench.Write(out, name, results)
		if werr != nil {
			return werr
		}
		fmt.Printf("отчёт: %s, %s\n", filepath.ToSlash(md), filepath.ToSlash(js))
		fmt.Print(bench.Markdown(results))
	}
	if err != nil {
		return err
	}
	for _, r := range results {
		if !r.Passed() {
			os.Exit(1)
		}
	}
	return nil
}
