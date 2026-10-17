package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aitr/34-files-agent/internal/agent"
	"aitr/34-files-agent/internal/cite"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/features"
	"aitr/34-files-agent/internal/help"
	"aitr/34-files-agent/internal/kb"
	"aitr/34-files-agent/internal/llm"
	"aitr/34-files-agent/internal/project"
	"aitr/34-files-agent/internal/ps"
	"aitr/34-files-agent/internal/tools"
)

// Общее у команд, которые зовут модель (help, serve, bench): клиент из
// окружения, сборка project.Deps, журнал хода в консоль.

// Переменные окружения модели.
const (
	envDeepSeekKey   = "DEEPSEEK_API_KEY"
	envDeepSeekBase  = "DEEPSEEK_BASE_URL"
	envDeepSeekModel = "DEEPSEEK_MODEL"
	envLocalBase     = "LOCAL_LLM_BASE_URL"
	envLocalModel    = "LOCAL_LLM_MODEL"
	envLocalReason   = "LOCAL_LLM_REASONING"
	envProvider      = "LLM_PROVIDER"

	defaultLocalBase  = "http://127.0.0.1:11434"
	defaultLocalModel = "qwen3.5:9b"
	// localMaxTokens — предел ответа локальной модели: без него она при
	// зацикливании пишет до конца окна.
	localMaxTokens = 4096
	localTimeout   = 10 * time.Minute
)

// loadDotEnv подхватывает KEY=value из .env.local и .env (в каталоге root
// и выше, затем от текущего каталога и выше): ключ курса лежит в корне, а
// команда запускается из tools/curator или из снимка в задании. Уже
// заданные переменные не перетираются; значения никуда не печатаются.
func loadDotEnv(root string) {
	seen := map[string]bool{}
	walk := func(dir string) {
		for {
			if seen[dir] {
				return
			}
			seen[dir] = true
			loadEnvFile(filepath.Join(dir, ".env.local"))
			loadEnvFile(filepath.Join(dir, ".env"))
			parent := filepath.Dir(dir)
			if parent == dir {
				return
			}
			dir = parent
		}
	}
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			walk(abs)
		}
	}
	if wd, err := os.Getwd(); err == nil {
		walk(wd)
	}
}

func loadEnvFile(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), ""))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if key == "" || value == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}
}

// curatorLLM — клиент модели по провайдеру: cloud (DeepSeek, ключ
// DEEPSEEK_API_KEY) или local (OpenAI-совместимый сервер LOCAL_LLM_BASE_URL,
// модель LOCAL_LLM_MODEL, рассуждение выключено — LOCAL_LLM_REASONING).
// Пусто — LLM_PROVIDER или cloud. Ключ не печатается.
func curatorLLM(provider string) (llm.Chatter, string, error) {
	if strings.TrimSpace(provider) == "" {
		provider = os.Getenv(envProvider)
	}
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "local", "ollama":
		base := strings.TrimSpace(os.Getenv(envLocalBase))
		if base == "" {
			base = defaultLocalBase
		}
		base = strings.TrimSuffix(strings.TrimRight(base, "/"), "/v1")
		if !strings.Contains(base, "://") {
			base = "http://" + base
		}
		model := strings.TrimSpace(os.Getenv(envLocalModel))
		if model == "" {
			model = defaultLocalModel
		}
		cl := llm.NewClient("local", base+"/v1")
		cl.HTTP = &http.Client{Timeout: localTimeout}
		cl.DefaultMaxTokens = localMaxTokens
		reasoning := strings.TrimSpace(os.Getenv(envLocalReason))
		switch reasoning {
		case "":
			cl.ReasoningEffort = "none"
		case "-":
		default:
			cl.ReasoningEffort = reasoning
		}
		llm.RegisterLocal(model)
		return cl, model, nil
	case "", "cloud", "deepseek":
		key := strings.TrimSpace(os.Getenv(envDeepSeekKey))
		if key == "" {
			return nil, "", errors.New("нужен " + envDeepSeekKey + " — в окружении или в .env/.env.local корня курса; локальная модель — -llm local")
		}
		model := strings.TrimSpace(os.Getenv(envDeepSeekModel))
		if model == "" {
			model = llm.DefaultModel
		}
		return llm.NewClient(key, os.Getenv(envDeepSeekBase)), model, nil
	}
	return nil, "", fmt.Errorf("неизвестный провайдер %q (cloud, local)", provider)
}

// curatorDeps — зависимости инструментов проекта: корень курса, база
// документации (если собрана), поиск, исполнитель aitr. Базы нет — не
// ошибка: /help отвечает без неё и говорит об этом в журнале. closeFn
// закрывает базу.
func curatorDeps(ctx context.Context, rootFlag, dataFlag, index string) (project.Deps, func(), error) {
	root, err := openRoot(rootFlag)
	if err != nil {
		return project.Deps{}, func() {}, err
	}
	d := project.Deps{Root: root, Index: index, PS: ps.Aitr(root.Dir)}
	if d.Index == "" {
		d.Index = string(kb.Structure)
	}
	path := dbPath(dataDir(root, dataFlag))
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintf(os.Stderr, "база документации %s не найдена — ответы без источников; соберите: curator index\n", filepath.ToSlash(path))
		return d, func() {}, nil
	}
	store, err := kb.Open(ctx, path)
	if err != nil {
		return project.Deps{}, func() {}, fmt.Errorf("база %s: %w", path, err)
	}
	if _, err := store.Index(ctx, d.Index); err != nil {
		store.Close()
		fmt.Fprintf(os.Stderr, "в базе %s нет индекса %s — ответы без источников; соберите: curator index\n", filepath.ToSlash(path), d.Index)
		return d, func() {}, nil
	}
	d.Store = store
	d.Searcher = &kb.Searcher{Store: store, Embedder: pickSearchEmbedder(ctx)}
	return d, func() { store.Close() }, nil
}

// pickSearchEmbedder — эмбеддер для вопросов: из окружения, если
// отвечает; иначе nil — kb сам уходит в BM25 и говорит об этом в ответе
// docs_search.
func pickSearchEmbedder(ctx context.Context) embed.Embedder {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(embed.EnvBaseURL)), "none") {
		return nil
	}
	h := embed.FromEnv()
	hctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if st := h.Health(hctx); !st.OK {
		return nil
	}
	return h
}

// parseFeatures — набор механизмов по строке флага («-help.rag», «all»,
// «default»); пусто — умолчания реестра.
func parseFeatures(spec string) (features.Set, error) {
	reg := features.Catalog()
	if strings.TrimSpace(spec) == "" {
		return reg.Defaults(), nil
	}
	return reg.Parse(spec, reg.Defaults())
}

// consoleJournal — журнал хода в stderr: вызовы инструментов, отказы
// завершающего, запросы к модели. Ответы инструментов целиком не печатает.
type consoleJournal struct{ quiet bool }

func (c consoleJournal) Publish(agent.Update) {}

func (c consoleJournal) Log(e agent.Event) {
	if c.quiet {
		return
	}
	switch e.Kind {
	case agent.EventToolCall:
		by := ""
		if strings.Contains(e.Title, "кодом") {
			by = " (кодом до запроса)"
		}
		fmt.Fprintf(os.Stderr, "  → %s%s %s\n", e.Tool, by, oneLine(e.Detail, 100))
	case agent.EventToolResult:
		if e.Final {
			fmt.Fprintf(os.Stderr, "  ✓ %s: принят\n", e.Tool)
			return
		}
		fmt.Fprintf(os.Stderr, "  ← %s (%.2f с)\n", e.Title, e.Seconds)
	case agent.EventToolError:
		mark := "✗"
		if e.Rejected {
			mark = "⟲"
		}
		fmt.Fprintf(os.Stderr, "  %s %s: %s\n", mark, e.Tool, oneLine(e.Detail, 300))
	case agent.EventLLMRequest:
		fmt.Fprintf(os.Stderr, "  … запрос к модели %d\n", e.Step)
	case agent.EventLLMReply:
		u := ""
		if e.Usage != nil {
			u = fmt.Sprintf(", %d токенов", e.Usage.Total)
		}
		fmt.Fprintf(os.Stderr, "  ← %s (%.1f с%s)\n", e.Title, e.Seconds, u)
	case agent.EventMechanism, agent.EventNote:
		fmt.Fprintf(os.Stderr, "  · %s\n", e.Title)
	}
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// docsSearchFor — docs_search над базой для роли course без агента (MCP
// и прочие потребители Deps.DocsSearch); у /help свой, с наблюдателем
// выдачи хода.
func docsSearchFor(d project.Deps) tools.Tool {
	if d.Searcher == nil {
		return nil
	}
	return cite.SearchTool(d.Searcher, d.Index, help.DocsK, nil)
}

// rootDir — каталог корня или пусто.
func rootDir(r *course.Root) string {
	if r == nil {
		return ""
	}
	return r.Dir
}

// docs_search для MCP-сервера course — тот же поиск с цитатами, что у
// агента /help (без наблюдателя выдачи: у сервера нет хода).
func init() {
	DocsSearchTool = func(s *kb.Searcher, index string) tools.Tool {
		return cite.SearchTool(s, index, help.DocsK, nil)
	}
}
