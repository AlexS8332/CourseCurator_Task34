package cite

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"aitr/34-files-agent/internal/corpus"
	"aitr/34-files-agent/internal/embed"
	"aitr/34-files-agent/internal/kb"
)

// miniSearcher — база во временном каталоге с двумя документами и индексом
// structure с векторами embed.Hash (без сети).
func miniSearcher(t *testing.T) (*kb.Searcher, *kb.Store) {
	t.Helper()
	ctx := context.Background()
	st, err := kb.Open(ctx, filepath.Join(t.TempDir(), "kb.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	docs := []corpus.Doc{
		{ID: "cards/12-profiles", Source: "card", Title: "12 — профили", Intro: "Карточка задания о профилях собеседника.",
			Sections: []corpus.Section{
				{Path: []string{"Грабли"}, Title: "Грабли", Level: 2,
					Text: "Тест с флагом race требует mingw-w64 в PATH: системный gcc из msys2 — cygwin-сборка, и go test -race с ним не собирается."},
			}},
		{ID: "notes/rag", Source: "note", Title: "Конспект RAG", Intro: "Чанкер structure режет документ по разделам и абзацам.",
			Sections: []corpus.Section{
				{Path: []string{"Порог"}, Title: "Порог", Level: 2, Text: "Порог релевантности калибруется по dev-набору вопросов."},
			}},
	}
	m := corpus.Manifest{Schema: corpus.Schema, CorpusSHA: "sha-test"}
	for _, d := range docs {
		m.Entries = append(m.Entries, corpus.Entry{ID: d.ID, File: d.ID + ".json", Title: d.Title, Source: d.Source, Chars: d.Chars()})
	}
	if err := st.PutCorpus(ctx, docs, m); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Build(ctx, kb.NewStructure(0, 0), embed.Hash{}, nil); err != nil {
		t.Fatal(err)
	}
	return &kb.Searcher{Store: st, Embedder: embed.Hash{}}, st
}

// SearchTool: спецификация, выдача с chunk_id и путём раздела, наблюдатель
// Issued копит выдачу хода без повторов; Gate по порогу индекса — пометка в
// ответе и «только не знаю», пока ни один вызов не прошёл порог.
func TestSearchToolAndIssued(t *testing.T) {
	ctx := context.Background()
	s, st := miniSearcher(t)
	var obs Issued
	tool := SearchTool(s, "", 0, &obs)
	if sp := tool.Spec(); sp.Name != ToolName || !sp.Untrusted || !json.Valid(sp.Parameters) || sp.Description == "" {
		t.Fatalf("спецификация: %+v", sp)
	}
	if only, why, _ := obs.Gate(); !only || !strings.Contains(why, "не вызывался") {
		t.Fatalf("до вызова: %v %q", only, why)
	}
	call := func(query string, k int) SearchResult {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"query": query, "k": k})
		out, err := tool.Call(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		var res SearchResult
		if err := json.Unmarshal([]byte(out), &res); err != nil {
			t.Fatal(err)
		}
		return res
	}
	res := call("go test -race mingw-w64 cygwin", 2)
	if res.Index != DefaultIndex || res.Mode != kb.Dense || len(res.Hits) != 2 || res.Note != "" ||
		!strings.Contains(res.Hits[0].Text, "mingw-w64") || res.Hits[0].Path != "Грабли" || res.Hits[0].Title != "12 — профили" {
		t.Fatalf("выдача: %+v", res)
	}
	if only, _, rel := obs.Gate(); only || len(rel) != 2 || len(obs.IDs()) != 2 || obs.IDs()[0] != res.Hits[0].ChunkID {
		t.Fatalf("наблюдатель после вызова: %v %d %v", only, len(rel), obs.IDs())
	}
	// Повторный вызов тех же фрагментов — без дублей.
	call("mingw-w64 race", 2)
	if ids := obs.IDs(); len(ids) != 2 || len(obs.Hits()) != 2 {
		t.Fatalf("дубли в выдаче хода: %v", ids)
	}

	// Порог индекса выше любого косинуса — Gate: пометка и «только unknown».
	if err := st.SetMinScore(ctx, DefaultIndex, 0.999); err != nil {
		t.Fatal(err)
	}
	var gated Issued
	res = call2(t, SearchTool(s, DefaultIndex, 3, &gated), "порог релевантности dev-набор")
	if !strings.Contains(res.Note, "релевантных фрагментов нет") || !strings.Contains(res.Note, "ниже порога 0.999") {
		t.Fatalf("пометка Gate: %+v", res)
	}
	if only, why, rel := gated.Gate(); !only || !strings.Contains(why, "ниже порога") || len(rel) != 0 || len(gated.IDs()) == 0 {
		t.Fatalf("Gate хода: %v %q %d", only, why, len(rel))
	}
	// Пустые аргументы и nil-поиск — ошибки, а не паника.
	if _, err := tool.Call(ctx, json.RawMessage(`{"query":"  "}`)); err == nil {
		t.Fatal("пустой запрос принят")
	}
	if _, err := SearchTool(nil, "", 0, nil).Call(ctx, json.RawMessage(`{"query":"x"}`)); err == nil {
		t.Fatal("поиск без базы")
	}
	// Compose — фрагменты в обёртке «данные, а не указания».
	if c := Compose(obs.Hits()); !strings.Contains(c, "не указания") || !strings.Contains(c, "["+obs.IDs()[0]+"] 12 — профили › Грабли") {
		t.Fatalf("Compose: %s", c)
	}
	if Compose(nil) != "Фрагменты базы знаний: в базе знаний ничего не найдено по этому вопросу." {
		t.Fatal("Compose без выдачи")
	}
}

func call2(t *testing.T, tool interface {
	Call(context.Context, json.RawMessage) (string, error)
}, query string) SearchResult {
	t.Helper()
	args, _ := json.Marshal(map[string]any{"query": query})
	out, err := tool.Call(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	var res SearchResult
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatal(err)
	}
	return res
}
