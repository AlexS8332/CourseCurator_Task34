package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aitr/34-files-agent/internal/bench"
)

func init() {
	register("bench", "-trial 14|15|16|17 [-lanes docs,state] [-out dir] [-judge] [-root dir] [-data dir] [-llm cloud|local] [-questions eval/help.json] [-set eval/support|eval/review|eval/files]  стенд испытания", runBench)
}

// runBench — `curator bench -trial 14`: стенды И-14 на живой модели,
// отчёт markdown + JSON в -out (по умолчанию examples/help у текущего
// каталога). Код 1, если хоть один порог не прошёл. `-trial 15` — стенд
// И-15 (runBench15 в review.go), отчёт по умолчанию в examples/review;
// `-trial 16` — стенд И-16 (runBench16 в support.go), в examples/support;
// `-trial 17` — стенд И-17 (runBench17 в files.go), в examples/files.
func runBench(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("bench", flag.ContinueOnError)
	trial := fs.Int("trial", 14, "номер испытания: 14 (/help), 15 (ревью PR), 16 (поддержка) или 17 (файловый агент)")
	lanes := fs.String("lanes", "", "стенды через запятую: docs, state (пусто — оба)")
	out := fs.String("out", "examples/help", "куда писать отчёт (t14-<время>.md и .json)")
	judge := fs.Bool("judge", false, "судья-модель, отчётно (в день 31 не подключён)")
	rootFlag := fs.String("root", "", "корень курса")
	dataFlag := fs.String("data", "", "каталог данных с course.db")
	llmFlag := fs.String("llm", "", "провайдер модели: cloud или local")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	questions := fs.String("questions", "", "файл вопросов (по умолчанию eval/help.json рядом с модулем или у корня tools/curator)")
	set := fs.String("set", "", "каталог набора И-15/И-16/И-17 (по умолчанию eval/review, eval/support или eval/files рядом с модулем или у корня tools/curator)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	switch *trial {
	case 14:
	case 15:
		outDir := *out
		if outDir == "examples/help" {
			outDir = "examples/review"
		}
		return runBench15(ctx, *rootFlag, *dataFlag, *llmFlag, *indexFlag, *set, outDir)
	case 16:
		outDir := *out
		if outDir == "examples/help" {
			outDir = "examples/support"
		}
		return runBench16(ctx, *rootFlag, *dataFlag, *llmFlag, *indexFlag, *set, outDir)
	case 17:
		outDir := *out
		if outDir == "examples/help" {
			outDir = "examples/files"
		}
		return runBench17(ctx, *rootFlag, *dataFlag, *llmFlag, *indexFlag, *set, outDir)
	default:
		return fmt.Errorf("испытание %d ещё не реализовано (есть 14, 15, 16 и 17)", *trial)
	}
	loadDotEnv(*rootFlag)
	cl, model, err := curatorLLM(*llmFlag)
	if err != nil {
		return err
	}
	d, closeDB, err := curatorDeps(ctx, *rootFlag, *dataFlag, *indexFlag)
	if err != nil {
		return err
	}
	defer closeDB()
	if d.Searcher == nil {
		return fmt.Errorf("для И-14 нужна база документации: curator index")
	}
	qpath := *questions
	if qpath == "" {
		qpath = findQuestions(rootDir(d.Root))
	}
	var stands []string
	for _, s := range strings.Split(*lanes, ",") {
		if s = strings.TrimSpace(s); s != "" {
			stands = append(stands, s)
		}
	}
	fmt.Printf("И-14 · модель %s · вопросы %s · корень %s\n", model, qpath, rootDir(d.Root))
	started := time.Now()
	results, err := bench.T14(ctx, bench.T14Options{Deps: d, LLM: cl, Model: model, Questions: qpath, Stands: stands, Judge: *judge, Log: os.Stdout})
	if len(results) > 0 {
		name := "t14-" + started.Format("20060102-150405")
		md, js, werr := bench.Write(*out, name, results)
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

// findQuestions — eval/help.json: рядом с текущим каталогом, у корня
// tools/curator или в снимке assignments/*/curator — первое, что есть.
func findQuestions(root string) string {
	candidates := []string{filepath.Join("eval", "help.json")}
	if root != "" {
		candidates = append(candidates, filepath.Join(root, "tools", "curator", "eval", "help.json"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "eval", "help.json"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}
