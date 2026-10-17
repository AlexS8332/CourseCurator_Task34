package main

import (
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
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/support"
)

func init() {
	register("support", `-ticket T-08 [-root dir] [-data dir] [-crm dir] [-llm cloud|local] [-features спец] [-json] [-quiet]  ответ поддержки по тикету CRM`, runSupport)
}

// runSupport — `curator support -ticket T-08`: один ход агента поддержки
// по тикету из CRM. CRM — каталог с users.json и tickets.json: -crm, иначе
// data/support у каталога данных; нет его — копия eval/support (набор не
// портится). Журнал — в stderr, ответ — в stdout (текст или -json: весь
// Turn с вызовами). Ход без принятого ответа — код 1, но escalate «не
// проверено» всё равно печатается.
func runSupport(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("support", flag.ContinueOnError)
	ticket := fs.String("ticket", "", "id тикета (T-08)")
	rootFlag := fs.String("root", "", "корень курса (по умолчанию — вверх от текущего каталога)")
	dataFlag := fs.String("data", "", "каталог данных с course.db (по умолчанию <корень>/tools/curator/data или $CURATOR_DATA)")
	crmFlag := fs.String("crm", "", "каталог CRM с users.json и tickets.json (по умолчанию <данные>/support; нет — копия eval/support)")
	llmFlag := fs.String("llm", "", "провайдер модели: cloud (DeepSeek) или local; пусто — $LLM_PROVIDER или cloud")
	featFlag := fs.String("features", "", "механизмы: «-support.context», «all», «default»")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	asJSON := fs.Bool("json", false, "печатать итог хода JSON (ответ, проверка, контекст, счётчики, вызовы)")
	quiet := fs.Bool("quiet", false, "без журнала хода в stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	id := strings.TrimSpace(*ticket)
	if id == "" && len(fs.Args()) > 0 {
		id = strings.TrimSpace(fs.Args()[0])
	}
	if id == "" {
		return errors.New("нужен тикет: curator support -ticket T-08")
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
	crm, err := crmDir(rootDir(d.Root), dataDir(d.Root, *dataFlag), *crmFlag)
	if err != nil {
		return err
	}
	store, err := support.Open(crm)
	if err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "модель %s · корень %s · CRM %s · механизмы %s\n", model, rootDir(d.Root), filepath.ToSlash(crm), set)
	}
	turn, runErr := support.Run(ctx, cl, d, store, id, consoleJournal{quiet: *quiet}, support.Options{Model: model, Features: set})
	if turn == nil {
		return runErr
	}
	if *asJSON {
		out := struct {
			*support.Turn
			Calls []help.Call `json:"calls"`
			Error string      `json:"error,omitempty"`
		}{Turn: turn, Calls: turn.Calls.Calls()}
		if runErr != nil {
			out.Error = runErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(out); err != nil {
			return err
		}
	} else {
		fmt.Println(turn.Text)
		st := turn.Stats
		fmt.Fprintf(os.Stderr, "\nдействие %s · запросов %d · вызовов %d · токенов %d · %s · отказов %d\n",
			orDash(turn.Reply.Action), st.Steps, st.ToolCalls, st.Usage.Total, usd(st.Cost), st.Rejected)
	}
	return runErr
}

// crmDir — каталог CRM: flag, иначе <data>/support; нет его — копия
// eval/support (рядом с текущим каталогом, у корня tools/curator или у
// исполняемого файла) в <data>/support.
func crmDir(root, data, flag string) (string, error) {
	if flag != "" {
		return flag, nil
	}
	dir := filepath.Join(data, "support")
	if _, err := os.Stat(filepath.Join(dir, support.TicketsFile)); err == nil {
		return dir, nil
	}
	src := findEvalSet(root)
	if _, err := os.Stat(filepath.Join(src, support.TicketsFile)); err != nil {
		return "", fmt.Errorf("CRM не найдена: ни %s, ни набора eval/support (%s)", filepath.ToSlash(dir), filepath.ToSlash(src))
	}
	if err := bench.CopySet(src, dir); err != nil {
		return "", fmt.Errorf("копия eval/support в %s: %w", filepath.ToSlash(dir), err)
	}
	fmt.Fprintf(os.Stderr, "CRM %s создана копией набора %s\n", filepath.ToSlash(dir), filepath.ToSlash(src))
	return dir, nil
}

// findEvalSet — каталог eval/support: рядом с текущим каталогом, у корня
// tools/curator или у исполняемого файла — первое, что есть.
func findEvalSet(root string) string {
	candidates := []string{filepath.Join("eval", "support")}
	if root != "" {
		candidates = append(candidates, filepath.Join(root, "tools", "curator", "eval", "support"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "eval", "support"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, support.TicketsFile)); err == nil {
			return c
		}
	}
	return candidates[0]
}

// runBench16 — `curator bench -trial 16`: стенд И-16 на живой модели по
// набору set (eval/support), копии дорожек — во временном каталоге,
// отчёт — в out. Код 1, если хоть один порог не прошёл.
func runBench16(ctx context.Context, rootFlag, dataFlag, llmFlag, indexFlag, set, out string) error {
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
	if d.Searcher == nil {
		return fmt.Errorf("для И-16 нужна база документации: curator index")
	}
	if set == "" {
		set = findEvalSet(rootDir(d.Root))
	}
	fmt.Printf("И-16 · модель %s · набор %s · корень %s\n", model, filepath.ToSlash(set), rootDir(d.Root))
	started := time.Now()
	results, err := bench.T16(ctx, bench.T16Options{Deps: d, LLM: cl, Model: model, Set: set, Log: os.Stdout})
	if len(results) > 0 {
		name := "t16-" + started.Format("20060102-150405")
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

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}
