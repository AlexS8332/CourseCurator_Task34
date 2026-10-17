package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/llm"
)

func init() {
	register("help", `"вопрос" [-root dir] [-data dir] [-llm cloud|local] [-features спец] [-json] [-quiet]  ответ ассистента каталога с источниками`, runHelp)
}

// runHelp — `curator help "вопрос"`: один ход агента /help в консоли.
// Журнал хода — в stderr, ответ — в stdout (текст или -json: весь Turn).
// Ход без принятого ответа — код 1, но «не знаю» с пометкой всё равно
// печатается.
func runHelp(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("help", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "корень курса (по умолчанию — вверх от текущего каталога)")
	dataFlag := fs.String("data", "", "каталог данных с course.db (по умолчанию <корень>/tools/curator/data или $CURATOR_DATA)")
	llmFlag := fs.String("llm", "", "провайдер модели: cloud (DeepSeek) или local; пусто — $LLM_PROVIDER или cloud")
	featFlag := fs.String("features", "", "механизмы: «-help.rag», «-help.tools», «all», «default»")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	asJSON := fs.Bool("json", false, "печатать итог хода JSON (ответ, проверка, счётчики, вызовы)")
	quiet := fs.Bool("quiet", false, "без журнала хода в stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Флаги разрешены и после вопроса: `curator help "вопрос" -root …`.
	// Пакет flag останавливается на первом позиционном аргументе, поэтому
	// хвост разбирается ещё раз, а вопрос собирается из позиционных частей.
	var parts []string
	rest := fs.Args()
	for len(rest) > 0 {
		parts = append(parts, rest[0])
		rest = rest[1:]
		if len(rest) > 0 && strings.HasPrefix(rest[0], "-") {
			if err := fs.Parse(rest); err != nil {
				return err
			}
			rest = fs.Args()
		}
	}
	q := help.Clean(strings.Join(parts, " "))
	if q == "" {
		return errors.New("нужен вопрос: curator help \"на какой ветке корень?\"")
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
	d.DocsSearch = docsSearchFor(d)

	if !*quiet {
		fmt.Fprintf(os.Stderr, "модель %s · корень %s · механизмы %s\n", model, rootDir(d.Root), set)
	}
	turn, runErr := help.Run(ctx, cl, d, q, consoleJournal{quiet: *quiet}, help.Options{Model: model, Features: set})
	if turn == nil {
		return runErr
	}
	if *asJSON {
		out := struct {
			*help.Turn
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
		fmt.Fprintf(os.Stderr, "\nзапросов %d · вызовов %d · токенов %d · %s · отказов %d\n",
			st.Steps, st.ToolCalls, st.Usage.Total, usd(st.Cost), st.Rejected)
	}
	return runErr
}

// usd — цена строкой; неизвестный прайс — «?».
func usd(c llm.Cost) string {
	if !c.Known {
		return "?"
	}
	return fmt.Sprintf("$%.4f", c.USD)
}
