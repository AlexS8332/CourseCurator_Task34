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
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/review"
)

func init() {
	register("review", `[-range main...HEAD | -pr N | -patch file] [-post] [-out file.md] [-repo owner/name] [-root dir] [-data dir] [-llm cloud|local] [-features спец] [-json] [-quiet]  AI-ревью PR корня курса`, runReview)
}

// Переменные окружения GitHub (в Actions заданы автоматически).
const (
	envGitHubRepo  = "GITHUB_REPOSITORY"
	envGitHubToken = "GITHUB_TOKEN"
	envGitHubAPI   = "GITHUB_API_URL"
	// maxDiffBytes — предел diff из диапазона git.
	maxDiffBytes = 512 * 1024
)

// runReview — `curator review -pr 7 -post`: один ход агента-ревьюера.
// Вход — диапазон git (-range, по умолчанию незакоммиченные изменения
// корня относительно HEAD), PR GitHub (-pr с -repo или GITHUB_REPOSITORY,
// токен GITHUB_TOKEN) или файл патча (-patch). Журнал — в stderr, ревью —
// в stdout markdown (или -json: весь Turn) и в файл -out. -post публикует
// ревью в PR комментарием с inline-замечаниями; ревью с маркером этого
// sha уже есть — второй раз не постится. Ход без принятого ревью — код 1.
func runReview(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	rng := fs.String("range", "", "диапазон git (base...head); пусто — незакоммиченные изменения корня")
	pr := fs.Int("pr", 0, "номер pull request в GitHub")
	patch := fs.String("patch", "", "файл патча (unified diff) вместо git и GitHub")
	post := fs.Bool("post", false, "опубликовать ревью в PR (нужен -pr и GITHUB_TOKEN)")
	out := fs.String("out", "", "записать markdown ревью в файл")
	repoFlag := fs.String("repo", "", "репозиторий GitHub owner/name (по умолчанию $GITHUB_REPOSITORY)")
	rootFlag := fs.String("root", "", "корень курса (по умолчанию — вверх от текущего каталога)")
	dataFlag := fs.String("data", "", "каталог данных с course.db (по умолчанию <корень>/tools/curator/data или $CURATOR_DATA)")
	llmFlag := fs.String("llm", "", "провайдер модели: cloud (DeepSeek) или local; пусто — $LLM_PROVIDER или cloud")
	featFlag := fs.String("features", "", "механизмы: «-review.rag», «all», «default»")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	asJSON := fs.Bool("json", false, "печатать итог хода JSON (ревью, проверка, счётчики, вызовы)")
	quiet := fs.Bool("quiet", false, "без журнала хода в stderr")
	if err := fs.Parse(args); err != nil {
		return err
	}
	// Флаги разрешены и после позиционного аргумента: `curator review
	// main...HEAD -post`; позиционный — диапазон или файл патча.
	rest := fs.Args()
	for len(rest) > 0 {
		arg := rest[0]
		rest = rest[1:]
		switch {
		case strings.HasPrefix(arg, "-"):
			if err := fs.Parse(append([]string{arg}, rest...)); err != nil {
				return err
			}
			rest = fs.Args()
		case *patch == "" && fileExists(arg):
			*patch = arg
		case *rng == "":
			*rng = arg
		default:
			return fmt.Errorf("лишний аргумент %q", arg)
		}
	}
	if *post && *pr <= 0 {
		return errors.New("-post требует -pr N: публиковать можно только в pull request GitHub")
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

	var in review.Input
	var gh review.GitHub
	var owner, name string
	switch {
	case *patch != "":
		in, err = review.FromPatch(*patch, "")
	case *pr > 0:
		owner, name, err = ghRepo(*repoFlag)
		if err != nil {
			return err
		}
		gh = review.NewGitHub(os.Getenv(envGitHubAPI), ghToken())
		in, err = review.FromPR(ctx, gh, owner, name, *pr)
	default:
		in, err = review.FromRange(ctx, gitx.Repo{Dir: d.Root.Dir}, *rng, maxDiffBytes)
	}
	if err != nil {
		return err
	}
	if !*quiet {
		fmt.Fprintf(os.Stderr, "модель %s · корень %s · механизмы %s · файлов в diff %d\n", model, rootDir(d.Root), set, len(in.Files))
	}
	turn, runErr := review.Run(ctx, cl, d, in, consoleJournal{quiet: *quiet}, review.Options{Model: model, Features: set})
	if turn == nil {
		return runErr
	}
	md := turn.Markdown()
	if *out != "" {
		if err := os.WriteFile(*out, []byte(md), 0o644); err != nil {
			return err
		}
	}
	if *asJSON {
		o := struct {
			*review.Turn
			Markdown string `json:"markdown"`
			Error    string `json:"error,omitempty"`
		}{Turn: turn, Markdown: md}
		if runErr != nil {
			o.Error = runErr.Error()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(o); err != nil {
			return err
		}
	} else {
		fmt.Println(md)
		st := turn.Stats
		fmt.Fprintf(os.Stderr, "\nвердикт %s · находок %d · запросов %d · вызовов %d · токенов %d · %s · отказов %d\n",
			orDash(turn.Review.Verdict), len(turn.Review.Findings), st.Steps, st.ToolCalls, st.Usage.Total, usd(st.Cost), st.Rejected)
	}
	if runErr != nil {
		return runErr
	}
	if *post {
		return postReview(ctx, gh, owner, name, *pr, in, turn, *quiet)
	}
	return nil
}

// postReview — публикация ревью в PR: дедуп по маркеру sha головы среди
// опубликованных ревью, тело — markdown с маркером, inline-комментарии —
// к строкам diff; GitHub отверг комментарии — тело публикуется без них.
func postReview(ctx context.Context, gh review.GitHub, owner, name string, n int, in review.Input, turn *review.Turn, quiet bool) error {
	sha := strings.TrimSpace(in.Head)
	if sha == "" {
		return errors.New("у PR нет sha головы — маркер дедупа не составить")
	}
	posted, err := gh.Reviews(ctx, owner, name, n)
	if err != nil {
		return fmt.Errorf("опубликованные ревью: %w", err)
	}
	if review.HasMarker(posted, sha) {
		fmt.Fprintf(os.Stderr, "ревью на %s уже опубликовано — повтор не постится\n", sha[:min(12, len(sha))])
		return nil
	}
	body := turn.Markdown() + "\n" + review.Marker(sha) + "\n"
	comments := turn.Comments()
	err = gh.PostReview(ctx, owner, name, n, body, comments, review.EventComment)
	if err != nil && len(comments) > 0 {
		fmt.Fprintf(os.Stderr, "ревью с %d inline-комментариями не принято (%v) — публикуется без них\n", len(comments), err)
		err = gh.PostReview(ctx, owner, name, n, body, nil, review.EventComment)
	}
	if err != nil {
		return fmt.Errorf("публикация ревью: %w", err)
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "ревью опубликовано в PR #%d (%d inline-комментариев)\n", n, len(comments))
	}
	return nil
}

// ghRepo — owner/name из флага или GITHUB_REPOSITORY.
func ghRepo(flag string) (string, string, error) {
	s := strings.TrimSpace(flag)
	if s == "" {
		s = strings.TrimSpace(os.Getenv(envGitHubRepo))
	}
	if s == "" {
		return "", "", errors.New("нужен репозиторий: -repo owner/name или $" + envGitHubRepo)
	}
	return review.SplitRepo(s)
}

// ghToken — GITHUB_TOKEN (в Actions) или GH_TOKEN (локально, gh auth).
// Значение не печатается.
func ghToken() string {
	for _, k := range []string{envGitHubToken, "GH_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// findEvalReview — каталог eval/review: рядом с текущим каталогом, у корня
// tools/curator или у исполняемого файла — первое, что есть.
func findEvalReview(root string) string {
	candidates := []string{filepath.Join("eval", "review")}
	if root != "" {
		candidates = append(candidates, filepath.Join(root, "tools", "curator", "eval", "review"))
	}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "eval", "review"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(filepath.Join(c, review.PlantedFile)); err == nil {
			return c
		}
	}
	return candidates[0]
}

// runBench15 — `curator bench -trial 15`: стенд И-15 на живой модели по
// набору set (eval/review), отчёт — в out. Код 1, если хоть один порог
// не прошёл.
func runBench15(ctx context.Context, rootFlag, dataFlag, llmFlag, indexFlag, set, out string) error {
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
		return fmt.Errorf("для И-15 нужна база документации: curator index")
	}
	if set == "" {
		set = findEvalReview(rootDir(d.Root))
	}
	fmt.Printf("И-15 · модель %s · набор %s · корень %s\n", model, filepath.ToSlash(set), rootDir(d.Root))
	started := time.Now()
	results, err := bench.T15(ctx, bench.T15Options{Deps: d, LLM: cl, Model: model, Set: set, Log: os.Stdout})
	if len(results) > 0 {
		name := "t15-" + started.Format("20060102-150405")
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
