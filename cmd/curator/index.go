package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/docs"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/kb"
)

func init() {
	register("index", "[-root dir] [-data dir] [-check] [-embedder url|none]  собрать индекс документации (data/course.db)", runIndex)
}

// runIndex — `curator index`: документы корня (docs.Load) и схемы
// (docs.GenSchemas в data/schema) → data/course.db, стратегия structure
// 1200/200, в метаданных — коммит и дерево корня. -check — не собирать, а
// сверить: код 1, если документы новее индекса. -embedder: адрес
// сайдкара/Ollama, «none» — только BM25, пусто — EMBED_BASE_URL или
// сайдкар по умолчанию, если он отвечает, иначе BM25.
func runIndex(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("index", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "корень курса (по умолчанию — вверх от текущего каталога)")
	dataFlag := fs.String("data", "", "каталог данных (по умолчанию <корень>/tools/curator/data или $CURATOR_DATA)")
	check := fs.Bool("check", false, "только проверить актуальность индекса: код 1, если устарел")
	embFlag := fs.String("embedder", "", "адрес эмбеддера, none — без векторов (BM25)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	root, err := openRoot(*rootFlag)
	if err != nil {
		return err
	}
	data := dataDir(root, *dataFlag)
	store, err := kb.Open(ctx, dbPath(data))
	if err != nil {
		return err
	}
	defer store.Close()

	if *check {
		stale, why, err := docs.Stale(ctx, store, root)
		if err != nil {
			return err
		}
		fmt.Printf("индекс %s: %s\n", filepath.ToSlash(dbPath(data)), why)
		if stale {
			os.Exit(1)
		}
		return nil
	}

	started := time.Now()
	loaded, err := docs.Load(root)
	if err != nil {
		return err
	}
	schemas, err := docs.GenSchemas(root, filepath.Join(data, "schema"))
	if err != nil {
		return err
	}
	all := append(loaded, schemas...)
	state := docs.State(ctx, root, loaded)

	emb, embNote := pickEmbedder(ctx, *embFlag)
	fmt.Printf("корень %s: документов %d, commit %s, tree %s\n", root.Dir, len(all), orNone(state.Commit), orNone(state.Tree))
	fmt.Printf("эмбеддер: %s\n", embNote)
	info, err := docs.Index(ctx, store, all, emb, state.Commit, state.Tree)
	if err != nil {
		return err
	}
	chunks, _ := store.Chunks(ctx, info.ID, "")
	fmt.Printf("индекс %s: %d документов, %d чанков, %d токенов, %.1f с → %s\n",
		info.ID, len(all), info.Chunks, info.Tokens, time.Since(started).Seconds(), filepath.ToSlash(dbPath(data)))
	fmt.Println(byKind(all, chunks))
	return nil
}

// pickEmbedder — эмбеддер по флагу: none → nil; адрес → HTTP с этим
// адресом; пусто → из окружения, но только если отвечает (иначе BM25:
// индексация без сайдкара не должна падать).
func pickEmbedder(ctx context.Context, flagv string) (embed.Embedder, string) {
	if strings.EqualFold(flagv, "none") {
		return nil, "нет (только BM25)"
	}
	h := embed.FromEnv()
	if flagv != "" {
		h.BaseURL = flagv
	}
	st := h.Health(ctx)
	if !st.OK {
		if flagv != "" {
			return nil, fmt.Sprintf("%s не отвечает (%s) — только BM25", h.BaseURL, st.Why)
		}
		return nil, fmt.Sprintf("не задан, %s не отвечает — только BM25", h.BaseURL)
	}
	return h, fmt.Sprintf("%s (%s, %d измерений)", h.BaseURL, st.Model, st.Dims)
}

// byKind — строка «карточек 31 (чанков 420), конспектов …» по видам.
func byKind(all []corpus.Doc, chunks []kb.Chunk) string {
	nd, nc := map[string]int{}, map[string]int{}
	for _, d := range all {
		nd[d.Source]++
	}
	for _, c := range chunks {
		nc[c.Source]++
	}
	var parts []string
	for _, k := range docs.Kinds {
		if n := nd[string(k)]; n > 0 {
			parts = append(parts, fmt.Sprintf("%s %d (чанков %d)", k, n, nc[string(k)]))
		}
	}
	return "по видам: " + strings.Join(parts, ", ")
}

func orNone(s string) string {
	if s == "" {
		return "(нет)"
	}
	return s
}
