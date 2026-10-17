package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/docs"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/files"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/review"
	"aitr/34-files-agent/internal/support"
	"aitr/34-files-agent/internal/web"
)

func init() {
	register("serve", "-addr 127.0.0.1:8781 [-root dir] [-data dir] [-crm dir] [-planted dir] [-range main...HEAD] [-llm cloud|local] [-git-init]  окно куратора (страница, /api/ask по SSE, /api/state, тикеты /api/support, ревью /api/review, файлы /api/files)", runServe)
}

// runServe — `curator serve`: окно куратора. -git-init — если корень не
// репозиторий, сделать его таковым (копия каталога в сценарии записи:
// git_branch и свежесть индекса должны работать и на копии).
func runServe(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:8781", "адрес окна")
	rootFlag := fs.String("root", "", "корень курса (по умолчанию — вверх от текущего каталога)")
	dataFlag := fs.String("data", "", "каталог данных с course.db (по умолчанию <корень>/tools/curator/data, <корень>/data или $CURATOR_DATA)")
	llmFlag := fs.String("llm", "", "провайдер модели: cloud (DeepSeek) или local; пусто — $LLM_PROVIDER или cloud")
	featFlag := fs.String("features", "", "механизмы: «-help.rag», «-help.tools», «all», «default»")
	indexFlag := fs.String("index", "", "индекс базы (по умолчанию structure)")
	crmFlag := fs.String("crm", "", "каталог CRM поддержки (users.json, tickets.json); по умолчанию <данные>/support, при отсутствии копируется из eval/support")
	plantedFlag := fs.String("planted", "", "каталог подставных PR (prs.json, planted/*.patch); по умолчанию <корень>/tools/curator/eval/review, затем ./eval/review")
	rangeFlag := fs.String("range", web.DefaultRange, "диапазон git для пункта «текущая ветка» вкладки «Ревью»")
	gitInit := fs.Bool("git-init", false, "сделать корень git-репозиторием (ветка main, один коммит), если он им не является")
	if err := fs.Parse(args); err != nil {
		return err
	}
	loadDotEnv(*rootFlag)
	root, err := openRoot(*rootFlag)
	if err != nil {
		return err
	}
	if *gitInit {
		if err := ensureGit(ctx, root.Dir); err != nil {
			return err
		}
	}
	cl, model, err := curatorLLM(*llmFlag)
	if err != nil {
		// Окно поднимается и без модели: страница и состояние работают,
		// вопрос ответит 503 с причиной.
		fmt.Fprintln(os.Stderr, "модель не настроена:", err)
		cl, model = nil, ""
	}
	set, err := parseFeatures(*featFlag)
	if err != nil {
		return err
	}
	data := *dataFlag
	if data == "" {
		// Копия каталога курса (сценарий записи) держит базу в <корень>/data.
		if _, err := os.Stat(dbPath(filepath.Join(root.Dir, "data"))); err == nil {
			if _, err := os.Stat(dbPath(dataDir(root, ""))); err != nil {
				data = filepath.Join(root.Dir, "data")
			}
		}
	}
	d, closeDB, err := curatorDeps(ctx, root.Dir, data, *indexFlag)
	if err != nil {
		return err
	}
	defer closeDB()
	d.DocsSearch = docsSearchFor(d)

	s := &web.Server{Deps: d, LLM: cl, Model: model, Features: set}
	crm, crmDir, crmErr := openCRM(root, dataDir(root, data), *crmFlag)
	if crmErr != nil {
		fmt.Fprintln(os.Stderr, "CRM поддержки не открыта:", crmErr)
	} else {
		s.Support = supportFor(crm, cl, d, set, model)
	}
	plantedDir := findDir(*plantedFlag, filepath.Join(root.Dir, "tools", "curator", "eval", "review"), filepath.Join("eval", "review"))
	examplesDir := findDir("", filepath.Join(root.Dir, "tools", "curator", "examples", "review"), filepath.Join("examples", "review"))
	s.Review = reviewFor(root, plantedDir, *rangeFlag, examplesDir, cl, d, set, model)
	s.Files = filesFor(cl, d, set, model)
	s.State = func(ctx context.Context) (web.State, error) {
		st := web.State{Model: model}
		if cl == nil {
			st.Note = "модель не настроена: DEEPSEEK_API_KEY или -llm local"
		}
		if crmErr != nil {
			st.Note = joinNotes(st.Note, "CRM: "+crmErr.Error())
		}
		if b, err := (gitx.Repo{Dir: root.Dir}).Branch(ctx); err == nil {
			st.Branch = strings.TrimSpace(b)
		}
		if d.Store == nil {
			st.Stale = true
			st.Note = joinNotes(st.Note, "база документации не собрана: curator index")
			return st, nil
		}
		if list, err := d.Store.Docs(ctx); err == nil {
			st.Index.Docs = len(list)
		}
		if idx, err := d.Store.Index(ctx, d.Index); err == nil {
			st.Index.Chunks = idx.Chunks
		}
		if meta, err := docs.ReadMeta(ctx, d.Store); err == nil {
			st.Index.Commit = meta.Commit
			if st.Index.Docs == 0 {
				st.Index.Docs = meta.Docs
			}
		}
		stale, why, err := docs.Stale(ctx, d.Store, root)
		if err != nil {
			st.Note = joinNotes(st.Note, "свежесть индекса: "+err.Error())
		} else {
			st.Stale = stale
			if stale {
				st.Note = joinNotes(st.Note, why)
			}
		}
		return st, nil
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	fmt.Printf("куратор: http://%s/ · корень %s · модель %s · механизмы %s · CRM %s · подставные PR %s · файлы: worktree в %s\n", ln.Addr(), root.Dir, orNone(model), set, orNone(crmDir), orNone(plantedDir), filepath.ToSlash(filepath.Join(root.Dir, "sandbox", "agent")))
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// ensureGit — корень без .git становится репозиторием с веткой main и
// одним коммитом «snapshot»: автор задаётся на команде, глобальный config
// не трогается.
func ensureGit(ctx context.Context, dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
		return nil
	}
	if err := gitx.Available(); err != nil {
		return err
	}
	run := func(args ...string) error {
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("git %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	if err := run("init", "-q"); err != nil {
		return err
	}
	if err := run("symbolic-ref", "HEAD", "refs/heads/main"); err != nil {
		return err
	}
	if err := run("add", "-A"); err != nil {
		return err
	}
	if err := run("-c", "user.name=curator", "-c", "user.email=curator@local", "commit", "-q", "-m", "snapshot", "--no-verify"); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "git: %s — репозиторий создан, ветка main\n", dir)
	return nil
}

func joinNotes(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	}
	return a + "; " + b
}

// openCRM — хранилище тикетов: -crm, иначе <данные>/support; каталога без
// tickets.json нет — создаётся копией eval/support (из корня курса или из
// каталога модуля), чтобы окно работало сразу после clone, а набор стенда
// оставался нетронутым.
func openCRM(root *course.Root, data, flag string) (*support.Store, string, error) {
	dir := flag
	if dir == "" {
		dir = filepath.Join(data, "support")
	}
	if _, err := os.Stat(filepath.Join(dir, "tickets.json")); err != nil {
		src := ""
		for _, cand := range []string{filepath.Join(root.Dir, "tools", "curator", "eval", "support"), filepath.Join("eval", "support")} {
			if _, err := os.Stat(filepath.Join(cand, "tickets.json")); err == nil {
				src = cand
				break
			}
		}
		if src == "" {
			return nil, dir, fmt.Errorf("%s: нет tickets.json, и eval/support для копии не найден", dir)
		}
		if err := copyCRM(src, dir); err != nil {
			return nil, dir, err
		}
		fmt.Fprintf(os.Stderr, "CRM: %s создан копией %s\n", dir, src)
	}
	st, err := support.Open(dir)
	if err != nil {
		return nil, dir, err
	}
	return st, dir, nil
}

// copyCRM — users.json и tickets.json из src в dst.
func copyCRM(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"users.json", "tickets.json"} {
		b, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dst, name), b, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// supportFor — вкладка «Тикеты» для окна: ход — support.Run, блок
// контекста карточки — support.Gather (нет, если механизм support.context
// выключен). Без модели ход не подставляется — окно ответит 503 с
// причиной, список и карточки работают.
func supportFor(crm *support.Store, cl llm.Chatter, d project.Deps, set features.Set, model string) *web.Support {
	sp := &web.Support{Store: crm}
	sp.Context = func(ctx context.Context, id string) (string, error) {
		if !set.On(features.SupportContext) {
			return "", errors.New("механизм support.context выключен")
		}
		c, err := support.Gather(ctx, d, crm, id)
		if err != nil {
			return "", err
		}
		return c.Block(), nil
	}
	if cl == nil {
		return sp
	}
	sp.Run = func(ctx context.Context, id string, em agent.Emitter) (*web.SupportTurn, error) {
		t, err := support.Run(ctx, cl, d, crm, id, em, support.Options{Features: set, Model: model})
		if t == nil {
			return nil, err
		}
		st := &web.SupportTurn{Reply: t.Reply, Check: t.Check, Stats: t.Stats, Context: t.Context.Block(), Hits: t.Hits, Features: t.Features, Applied: t.Applied}
		if t.Calls != nil {
			st.Calls = t.Calls.Calls()
		}
		return st, err
	}
	return sp
}

// findDir — каталог по флагу, иначе первый из кандидатов, который есть;
// нет ни одного — первый кандидат (окно скажет, чего не хватает).
func findDir(flag string, cands ...string) string {
	if flag != "" {
		return flag
	}
	for _, c := range cands {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			return c
		}
	}
	if len(cands) > 0 {
		return cands[0]
	}
	return ""
}

// reviewMaxBytes — предел diff «текущей ветки» для окна.
const reviewMaxBytes = 512 << 10

// reviewFor — вкладка «Ревью» для окна: список подставных PR —
// review.LoadPlanted, ход — review.Run по патчу подставного PR или по
// диапазону git корня. Без модели ход не подставляется — окно ответит
// 503 с причиной, список и панель Action работают.
func reviewFor(root *course.Root, plantedDir, rng, examplesDir string, cl llm.Chatter, d project.Deps, set features.Set, model string) *web.Review {
	rv := &web.Review{Range: rng, ExamplesDir: examplesDir}
	load := func() ([]review.Planted, error) {
		if _, err := os.Stat(filepath.Join(plantedDir, "prs.json")); err != nil {
			return nil, nil
		}
		return review.LoadPlanted(plantedDir)
	}
	rv.Planted = func() ([]web.PlantedRow, error) {
		list, err := load()
		rows := []web.PlantedRow{}
		for _, p := range list {
			rows = append(rows, web.PlantedRow{ID: p.ID, Title: p.Title, Kind: p.Kind, Clean: p.Clean, File: p.File, Line: p.Line, Note: p.Note})
		}
		return rows, err
	}
	if cl == nil {
		return rv
	}
	rv.Run = func(ctx context.Context, req web.ReviewRequest, em agent.Emitter) (*web.ReviewTurn, error) {
		var in review.Input
		if req.Planted != "" {
			list, err := load()
			if err != nil {
				return nil, err
			}
			var p *review.Planted
			for i := range list {
				if list[i].ID == req.Planted {
					p = &list[i]
					break
				}
			}
			if p == nil {
				return nil, fmt.Errorf("подставного PR нет: %s", req.Planted)
			}
			in, err = review.FromPatch(patchPath(plantedDir, p.Patch), p.Title)
			if err != nil {
				return nil, err
			}
			if in.Body == "" {
				in.Body = p.Body
			}
		} else {
			var err error
			in, err = review.FromRange(ctx, gitx.Repo{Dir: root.Dir}, req.Range, reviewMaxBytes)
			if err != nil {
				return nil, err
			}
		}
		t, err := review.Run(ctx, cl, d, in, em, review.Options{Features: set, Model: model})
		if t == nil {
			return nil, err
		}
		return reviewTurn(t, set), err
	}
	return rv
}

// patchPath — файл патча подставного PR: как задан, иначе в planted/ и в
// каталоге набора.
func patchPath(dir, patch string) string {
	if filepath.IsAbs(patch) {
		return patch
	}
	for _, c := range []string{patch, filepath.Join(dir, "planted", patch), filepath.Join(dir, patch)} {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return filepath.Join(dir, "planted", patch)
}

// reviewTurn — итог хода ревью в типах окна: diff по файлам и hunk'ам
// (kind ctx|add|del), находки, текст ревью.
func reviewTurn(t *review.Turn, set features.Set) *web.ReviewTurn {
	out := &web.ReviewTurn{Summary: t.Review.Summary, Verdict: t.Review.Verdict, Markdown: t.Markdown(), Check: t.Check, Rejected: t.Rejected,
		Unverified: t.Unverified, Stats: t.Stats, Calls: t.Calls, Hits: t.Hits, Features: set}
	out.Input = web.ReviewInput{Title: t.Input.Title, Body: t.Input.Body, Base: t.Input.Base, Head: t.Input.Head, Range: t.Input.Range,
		Number: t.Input.Number, Truncated: t.Input.Truncated, Files: diffFiles(t.Input.Files)}
	out.Findings = []web.ReviewFinding{}
	for _, f := range t.Review.Findings {
		out.Findings = append(out.Findings, web.ReviewFinding{File: f.File, Line: f.Line, Kind: f.Kind, Severity: f.Severity, Text: f.Text,
			Evidence: web.ReviewEvidence{ChunkID: f.Evidence.ChunkID, Quote: f.Evidence.Quote, Path: f.Evidence.Path, Line: f.Evidence.Line}})
	}
	return out
}

// diffFiles — diff в типах окна: файлы, hunk'и, строки kind ctx|add|del.
// Общий для вкладок «Ревью» и «Файлы».
func diffFiles(files []review.FileDiff) []web.DiffFile {
	out := []web.DiffFile{}
	for _, f := range files {
		df := web.DiffFile{Path: f.Path, OldPath: f.OldPath, Status: f.Status, Binary: f.Binary, Added: f.Added, Removed: f.Removed, Hunks: []web.DiffHunk{}}
		for _, h := range f.Hunks {
			dh := web.DiffHunk{Header: h.Header, OldStart: h.OldStart, OldLines: h.OldLines, NewStart: h.NewStart, NewLines: h.NewLines, Lines: []web.DiffLine{}}
			for _, l := range h.Lines {
				kind := "ctx"
				switch l.Kind {
				case '+':
					kind = "add"
				case '-':
					kind = "del"
				}
				dh.Lines = append(dh.Lines, web.DiffLine{Kind: kind, Old: l.Old, New: l.New, Text: l.Text})
			}
			df.Hunks = append(df.Hunks, dh)
		}
		out = append(out, df)
	}
	return out
}

// filesFor — вкладка «Файлы» для окна: прогоны сессии — files.Sessions
// (состояние между планом и согласием живёт на сервере по run_id), этапы
// — files.New/DoPlan/Approve/DoApply. Корень прогонов — корень сервера
// (-root; при -git-init worktree sandbox/agent/<run> ложится внутрь
// копии). Без модели этапы не подставляются — окно ответит 503 с
// причиной, список прогонов работает.
func filesFor(cl llm.Chatter, d project.Deps, set features.Set, model string) *web.Files {
	f := &web.Files{Gated: set.On(features.FilesGates), Features: set, WriteTools: files.WriteTools}
	ss := files.NewSessions()
	f.Runs = func() []web.FilesRun {
		list := ss.List()
		out := make([]web.FilesRun, 0, len(list))
		for i := len(list) - 1; i >= 0; i-- {
			out = append(out, *filesRunView(list[i]))
		}
		return out
	}
	if cl == nil {
		return f
	}
	opts := files.Options{Features: set, Model: model}
	f.Plan = func(ctx context.Context, goal string, em agent.Emitter) (*web.FilesRun, error) {
		r, err := files.New(ctx, d, goal, opts)
		if err != nil {
			return nil, err
		}
		ss.Put(r)
		err = r.DoPlan(ctx, cl, d, em, opts)
		return filesRunView(r), err
	}
	f.Approve = func(ctx context.Context, id, reply string, em agent.Emitter) (*web.FilesRun, bool, error) {
		r, ok := ss.Get(id)
		if !ok {
			return nil, false, fmt.Errorf("прогона нет: %s", id)
		}
		approved, err := r.Approve(ctx, reply, opts)
		if err != nil || !approved {
			return filesRunView(r), false, err
		}
		err = r.DoApply(ctx, cl, d, em, opts)
		return filesRunView(r), true, err
	}
	return f
}

// filesRunView — прогон в типах окна: план, worktree относительно корня,
// diff разобран по файлам и hunk'ам, счётчики, отчёт markdown.
func filesRunView(r *files.Run) *web.FilesRun {
	v := &web.FilesRun{ID: r.ID, Goal: r.Goal, Stage: string(r.Stage), Branch: r.Branch, Worktree: r.Worktree, Changed: append([]string{}, r.Changed...),
		Diff: []web.DiffFile{}, Commit: r.Commit, Stats: r.Stats, Calls: r.Calls, Unverified: r.Unverified, Seconds: r.Seconds,
		Counters: web.FilesCounters{WritesBeforeApprove: r.WritesBeforeApprove, WritesOutsidePlan: r.WritesOutsidePlan, Rejected: r.Rejected}}
	if r.Worktree != "" && r.Root != "" {
		if rel, err := filepath.Rel(r.Root, r.Worktree); err == nil && !strings.HasPrefix(rel, "..") {
			v.Worktree = filepath.ToSlash(rel)
		}
	}
	if r.Plan != nil {
		p := &web.FilesPlan{Goal: r.Plan.Goal, Steps: append([]string{}, r.Plan.Steps...), Writes: r.Plan.Writes, Files: []web.FilesPlanFile{}}
		for _, f := range r.Plan.Files {
			p.Files = append(p.Files, web.FilesPlanFile{Path: f.Path, New: f.New, Why: f.Why})
		}
		v.Plan = p
		v.Markdown = r.Report()
	}
	if r.Done != nil {
		v.Summary = r.Done.Summary
	}
	if strings.TrimSpace(r.Diff) != "" {
		if fd, err := review.ParseUnified(r.Diff); err == nil {
			v.Diff = diffFiles(fd)
		}
	}
	return v
}
