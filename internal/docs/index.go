package docs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/course"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/gitx"
	"aitr/34-files-agent/internal/kb"
)

// Параметры индекса — стратегия structure 1200/200, как у статей в v30:
// граница по заголовкам, длинный раздел режется по абзацам, короткий
// клеится с соседом.
const (
	IndexID = string(kb.Structure)
	MaxRune = 1200
	MinRune = 200
)

// metaID — служебная запись манифеста с метаданными сборки. kb.Store
// хранит манифест как есть, но произвольных полей у corpus.Manifest и
// kb.IndexInfo нет, а править kb нельзя, поэтому commit, tree и хеш
// документов едут в манифесте строкой с этим doc_id: документа с таким id
// в базе нет, kb.Store по записи манифеста ничего не ищет.
const metaID = "_curator/index"

// Meta — чем собран индекс: коммит и дерево корня на момент сборки, хеш
// содержимого документов (по нему считается устаревание) и время.
type Meta struct {
	Commit  string    `json:"commit"`
	Tree    string    `json:"tree"`
	DocsSHA string    `json:"docs_sha"`
	Docs    int       `json:"docs"`
	BuiltAt time.Time `json:"built_at"`
}

// ErrNoMeta — в базе нет индекса куратора.
var ErrNoMeta = errors.New("индекс документации ещё не собран")

// Index кладёт документы в базу (corpus_sha = хеш их содержимого, так
// kb сам сбрасывает индекс чужих документов) и собирает индекс structure;
// emb == nil — только BM25. commit и tree — состояние корня (gitx.Repo
// Head/Tree), для отчётов и окна.
func Index(ctx context.Context, store *kb.Store, docs []corpus.Doc, emb embed.Embedder, commit, tree string) (kb.IndexInfo, error) {
	if len(docs) == 0 {
		return kb.IndexInfo{}, errors.New("нет документов для индекса")
	}
	m := manifest(docs)
	meta := Meta{Commit: commit, Tree: tree, DocsSHA: docsOnlySHA(docs), Docs: len(docs), BuiltAt: time.Now().UTC()}
	raw, _ := json.Marshal(meta)
	m.Entries = append(m.Entries, corpus.Entry{ID: metaID, File: string(raw), Source: string(Schema)})
	if err := store.PutCorpus(ctx, docs, m); err != nil {
		return kb.IndexInfo{}, fmt.Errorf("документы в базу: %w", err)
	}
	info, err := store.Build(ctx, kb.NewStructure(MaxRune, MinRune), emb, nil)
	if err != nil {
		return kb.IndexInfo{}, err
	}
	return info, nil
}

// manifest — манифест документов: sha256 канонического текста каждого и
// общий corpus_sha по списку (id + sha), как у снимка Википедии.
func manifest(docs []corpus.Doc) corpus.Manifest {
	m := corpus.Manifest{Schema: corpus.Schema}
	ids := make([]string, 0, len(docs))
	shas := map[string]corpus.Entry{}
	for _, d := range docs {
		text := d.Text()
		sum := sha256.Sum256([]byte(text))
		e := corpus.Entry{ID: d.ID, File: d.ID, Title: d.Title, Source: d.Source,
			Chars: len([]rune(text)), SHA256: hex.EncodeToString(sum[:])}
		ids = append(ids, d.ID)
		shas[d.ID] = e
		m.Chars += e.Chars
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		e := shas[id]
		m.Entries = append(m.Entries, e)
		h.Write([]byte(id + "\x00" + e.SHA256 + "\n"))
	}
	m.CorpusSHA = hex.EncodeToString(h.Sum(nil))
	m.Pages = math.Round(float64(m.Chars)/corpus.PageChars*10) / 10
	return m
}

// DocsSHA — хеш содержимого документов, тот же, что Index кладёт в
// corpus_sha.
func DocsSHA(docs []corpus.Doc) string { return manifest(docs).CorpusSHA }

// ReadMeta — метаданные сборки из базы; ErrNoMeta, если индекса нет.
func ReadMeta(ctx context.Context, store *kb.Store) (Meta, error) {
	m, err := store.Manifest(ctx)
	if err != nil {
		return Meta{}, err
	}
	for _, e := range m.Entries {
		if e.ID != metaID {
			continue
		}
		var meta Meta
		if err := json.Unmarshal([]byte(e.File), &meta); err != nil {
			return Meta{}, fmt.Errorf("метаданные индекса: %w", err)
		}
		if _, err := store.Index(ctx, IndexID); err != nil {
			return meta, ErrNoMeta
		}
		return meta, nil
	}
	return Meta{}, ErrNoMeta
}

// Stale — устарел ли индекс: документы корня (Load) отличаются от
// проиндексированных. Сгенерированные схемы в сравнение не входят: они
// производные от тех же файлов. Второе значение — почему (или
// «актуален…»). Индекса нет — stale с причиной.
//
// Дерево git здесь не сравнивается: правка кода в tools/ меняет дерево,
// но не документацию, и гонять И-14 на таком индексе можно. Коммит и
// дерево сборки — в причине, для отчёта.
func Stale(ctx context.Context, store *kb.Store, root *course.Root) (bool, string, error) {
	meta, err := ReadMeta(ctx, store)
	if errors.Is(err, ErrNoMeta) {
		return true, "индекс не собран: curator index", nil
	}
	if err != nil {
		return true, "", err
	}
	docs, err := Load(root)
	if err != nil {
		return true, "", err
	}
	now := State(ctx, root, docs)
	if now.DocsSHA != meta.DocsSHA {
		changed, added, removed := diffDocs(ctx, store, docs)
		return true, fmt.Sprintf("документы изменились с %s (индекс собран %s на %s): изменено %d, добавлено %d, удалено %d",
			short(meta.Commit), meta.BuiltAt.Local().Format("02.01 15:04"), short(meta.Tree), changed, added, removed), nil
	}
	note := "актуален"
	if now.Commit != meta.Commit && now.Commit != "" {
		note += fmt.Sprintf(" (документы не менялись; индекс собран на %s, сейчас %s)", short(meta.Commit), short(now.Commit))
	}
	return false, note, nil
}

// Current — состояние корня: коммит и дерево (пусто, если корень не
// репозиторий или git недоступен) и хеш документов.
type Current struct {
	Commit, Tree, DocsSHA string
	Docs                  int
}

// State считает Current по уже загруженным документам (docs == nil —
// загружает сам). Схемы (GenSchemas) в хеш не входят: они производные.
func State(ctx context.Context, root *course.Root, docs []corpus.Doc) Current {
	if docs == nil {
		docs, _ = Load(root)
	}
	c := Current{DocsSHA: docsOnlySHA(docs), Docs: len(docs)}
	repo := gitx.Repo{Dir: root.Dir}
	if top, err := repo.Toplevel(ctx); err == nil && sameDir(top, root.Dir) {
		c.Commit, _ = repo.Head(ctx)
		c.Tree, _ = repo.Tree(ctx)
	}
	return c
}

// docsOnlySHA — хеш по документам без сгенерированных схем: Stale
// сравнивает то, что лежит в корне, а не слепки.
func docsOnlySHA(docs []corpus.Doc) string {
	var plain []corpus.Doc
	for _, d := range docs {
		if d.Source != string(Schema) {
			plain = append(plain, d)
		}
	}
	return DocsSHA(plain)
}

// diffDocs — сколько документов изменилось, появилось и пропало против
// базы (по sha256 манифеста).
func diffDocs(ctx context.Context, store *kb.Store, docs []corpus.Doc) (changed, added, removed int) {
	m, err := store.Manifest(ctx)
	if err != nil {
		return
	}
	was := map[string]string{}
	for _, e := range m.Entries {
		if e.ID != metaID && e.Source != string(Schema) {
			was[e.ID] = e.SHA256
		}
	}
	for _, e := range manifest(docs).Entries {
		sha, ok := was[e.ID]
		switch {
		case !ok:
			added++
		case sha != e.SHA256:
			changed++
		}
		delete(was, e.ID)
	}
	removed = len(was)
	return
}

func short(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	if sha == "" {
		return "(нет)"
	}
	return sha
}

func sameDir(a, b string) bool {
	norm := func(s string) string {
		return strings.ToLower(strings.TrimRight(strings.ReplaceAll(s, "\\", "/"), "/"))
	}
	return norm(a) == norm(b)
}
