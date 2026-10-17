package features

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogIsConsistent(t *testing.T) {
	r := Catalog()
	if len(r.All()) < 6 {
		t.Fatalf("в реестре %d механизмов", len(r.All()))
	}
	if err := r.Validate(r.Defaults()); err != nil {
		t.Fatalf("умолчания не согласованы: %v", err)
	}
	for _, m := range r.All() {
		if m.Title == "" || m.About == "" || m.Fallback == "" || m.Since == "" {
			t.Errorf("%s: у механизма не заполнено описание (Title/About/Fallback/Since)", m.Name)
		}
	}
	// Шесть механизмов дней 31–35 — по контракту, все включены по умолчанию.
	for _, n := range []Name{HelpRAG, HelpTools, ReviewRAG, SupportContext, FilesGates, CloseVerify} {
		m, ok := r.Get(n)
		if !ok || !m.Default {
			t.Errorf("%s: нет в реестре или выключен по умолчанию (%+v)", n, m)
		}
	}
}

// Order оставляет только включённые непустые блоки известных механизмов.
func TestOrderDropsDisabledEmptyAndUnknown(t *testing.T) {
	r := Catalog()
	got := r.Order([]Block{
		{Feature: SupportContext, Text: "   "},
		{Feature: "nope", Text: "x"},
		{Feature: HelpRAG, Text: "не блок"},
		{Feature: SupportContext, Text: "контекст"},
	}, r.AllOn())
	if len(got) != 1 || got[0].Feature != SupportContext || got[0].Text != "контекст" {
		t.Fatalf("остались %+v", got)
	}
	off := r.AllOn().With(SupportContext, false)
	if got := r.Order([]Block{{Feature: SupportContext, Text: "контекст"}}, off); len(got) != 0 {
		t.Fatalf("выключенный блок остался: %+v", got)
	}
}

func TestNewRejectsBrokenRegistries(t *testing.T) {
	cases := map[string][]Mechanism{
		"без имени":         {{Title: "x"}},
		"дубль":             {{Name: "a"}, {Name: "a"}},
		"блок без места":    {{Name: "a", Kind: KindBlock}},
		"место занято":      {{Name: "a", Kind: KindBlock, Place: 100}, {Name: "b", Kind: KindBlock, Place: 100}},
		"неизвестная связь": {{Name: "a", Requires: []Name{"b"}}},
	}
	for name, ms := range cases {
		if _, err := New(ms...); err == nil {
			t.Errorf("%s: реестр принят", name)
		}
	}
}

func TestDefaultsAreFullMap(t *testing.T) {
	r := Catalog()
	d := r.Defaults()
	for _, m := range r.All() {
		if !d.Known(m.Name) {
			t.Fatalf("%s нет в наборе по умолчанию", m.Name)
		}
		if d.On(m.Name) != m.Default {
			t.Fatalf("%s: %v, ждали %v", m.Name, d.On(m.Name), m.Default)
		}
	}
}

func TestParse(t *testing.T) {
	r := Catalog()
	s, err := r.Parse("-help.tools, +help.rag", r.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if s.On(HelpTools) || !s.On(HelpRAG) {
		t.Fatalf("набор %v", s)
	}
	s, err = r.Parse("none,help.rag", r.Defaults())
	if err != nil {
		t.Fatal(err)
	}
	if got := s.Names(); len(got) != 1 || got[0] != HelpRAG {
		t.Fatalf("none,help.rag → %v", got)
	}
	s, _ = r.Parse("all", Set{})
	if len(s.Names()) != len(r.All()) {
		t.Fatalf("all → %v", s.Names())
	}
	s, _ = r.Parse("none,default", Set{})
	if s.String() != r.Defaults().String() {
		t.Fatalf("default → %v", s)
	}
	if _, err := r.Parse("+help.tols", r.Defaults()); err == nil || !strings.Contains(err.Error(), "неизвестный механизм") {
		t.Fatalf("опечатка принята: %v", err)
	}
	// Пустая строка — набор не меняется, но дописывается до полной карты.
	s, _ = r.Parse("", NewSet(map[Name]bool{HelpRAG: true}))
	if !s.On(HelpRAG) || !s.Known(HelpTools) || s.On(HelpTools) {
		t.Fatalf("пустая строка: %v", s.Map())
	}
}

// Зависимости проверяются Validate: механизм без того, что он требует, —
// ошибка. В реестре куратора зависимостей нет, поэтому — на своём реестре.
func TestValidateRequirements(t *testing.T) {
	r, err := New(Mechanism{Name: "base"}, Mechanism{Name: "dep", Requires: []Name{"base"}})
	if err != nil {
		t.Fatal(err)
	}
	err = r.Validate(r.AllOn().With("base", false))
	if err == nil || !strings.Contains(err.Error(), "dep требует base") {
		t.Fatalf("зависимость без основания принята: %v", err)
	}
}

func TestDiffCountsOnlyRegistry(t *testing.T) {
	r := Catalog()
	a := r.Defaults()
	b := a.With(HelpTools, false).With("unknown-future", true)
	d := r.Diff(a, b)
	if len(d) != 1 || d[0] != HelpTools {
		t.Fatalf("Diff = %v", d)
	}
	if len(r.Diff(a, a)) != 0 {
		t.Fatal("разница набора с самим собой")
	}
}

func TestSetJSONKeepsUnknownAndSortsKeys(t *testing.T) {
	var s Set
	if err := json.Unmarshal([]byte(`{"zeta":true,"charter":false,"future.x":true}`), &s); err != nil {
		t.Fatal(err)
	}
	if !s.On("future.x") || s.On(Charter) || !s.Known(Charter) {
		t.Fatalf("прочитано %v", s.Map())
	}
	data, _ := json.Marshal(s)
	if string(data) != `{"charter":false,"future.x":true,"zeta":true}` {
		t.Fatalf("записано %s", data)
	}
	var empty Set
	data, _ = json.Marshal(empty)
	if string(data) != "{}" || !empty.Empty() {
		t.Fatalf("пустой набор: %s", data)
	}
	if err := json.Unmarshal([]byte(`[1]`), &s); err == nil {
		t.Fatal("массив принят за набор")
	}
}

func TestWithDoesNotMutate(t *testing.T) {
	a := NewSet(map[Name]bool{Charter: true})
	b := a.With(Charter, false)
	if !a.On(Charter) || b.On(Charter) {
		t.Fatal("With изменил исходный набор")
	}
	m := b.Map()
	m[Charter] = true
	if b.On(Charter) {
		t.Fatal("Map отдал внутреннюю карту")
	}
}

func TestComplete(t *testing.T) {
	r := Catalog()
	s := r.Complete(NewSet(map[Name]bool{HelpTools: true}))
	if !s.On(HelpTools) || !s.Known(HelpRAG) || s.On(HelpRAG) {
		t.Fatalf("Complete: %v", s.Map())
	}
}

func TestDescribeAndLabels(t *testing.T) {
	r := Catalog()
	st := r.Describe(r.Defaults().With(HelpTools, false))
	found := false
	for _, x := range st {
		if x.Name == HelpTools {
			found = true
			if x.On {
				t.Fatal("инструменты состояния отмечены включёнными")
			}
		}
	}
	if !found {
		t.Fatal("инструменты состояния не описаны")
	}
	if got := (KindBlock | KindCall).Labels(); strings.Join(got, ",") != "блок,вызов модели" {
		t.Fatalf("Labels = %v", got)
	}
	data, _ := json.Marshal(KindCheck)
	if string(data) != `["проверка"]` {
		t.Fatalf("Kind в JSON: %s", data)
	}
	if r2, _ := r.Get(SupportContext); r2.Place != PlaceState {
		t.Fatal("Get")
	}
}

// Цены блоков в реестре — у каждого механизма с блоком цена есть, и сумма
// блоков оставляет место под системный промпт, описания инструментов и
// окно в пределах постоянной части (6 тыс., раздел 10 ТЗ).
func TestCatalogBlockCostsMeasured(t *testing.T) {
	sum := 0
	for _, m := range Catalog().All() {
		if !m.Kind.Has(KindBlock) {
			continue
		}
		if m.Cost.Tokens <= 0 {
			t.Errorf("%s: блок без цены", m.Name)
		}
		sum += m.Cost.Tokens
	}
	if sum > 3000 {
		t.Fatalf("блоки по реестру ≈%d токенов: постоянной части в 6 тыс. не хватит на промпт и инструменты", sum)
	}
}
