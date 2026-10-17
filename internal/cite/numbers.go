package cite

// Разбор чисел ответа — вырез из v30 internal/rag/rule.go (token, parsed,
// normalize, tokenize, parse, multiplier, wordNumbers) и judge.go (clip):
// numbersMissing сверяет числа ответа с числами цитат тем же разбором, что
// правило оценки v22 («2,5» = «2.5», «5 тысяч» = «5 000», числа словами).

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// token — слово или число ответа. Дефис между числами («45–54») — отдельный
// токен-разделитель диапазона: он нужен множителю («2–3 тыс.»), а при
// сравнении форм пропускается.
type token struct {
	text  string
	num   bool
	value float64
	dash  bool
}

// parsed — ответ, разобранный для сравнения.
type parsed struct {
	norm   string
	toks   []token // без дефисов
	values []float64
}

// chunkRef — ссылка на фрагмент «manul/structure/004»: её цифры — не
// числа ответа (иначе «…/013» засчитал бы «13 часов»).
var chunkRef = regexp.MustCompile(`[\p{L}0-9][\p{L}0-9_-]*/(structure|fixed)/\d+`)

// thousands — число с разрядами через пробел: «5 000», «58 000».
var thousands = regexp.MustCompile(`\d{1,3}(?:[ \x{a0}\x{202f}\x{2009}]\d{3})+`)

// normalize — нижний регистр, ё→е, без ссылок на фрагменты, разряды
// склеены.
func normalize(s string) string {
	s = strings.ReplaceAll(strings.ToLower(s), "ё", "е")
	s = chunkRef.ReplaceAllString(s, " ")
	return collapseThousands(s)
}

// collapseThousands склеивает «5 000» в «5000», но только если группа
// стоит отдельно: «в 2024 500» или «1,500 000» не трогаются.
func collapseThousands(s string) string {
	locs := thousands.FindAllStringIndex(s, -1)
	if len(locs) == 0 {
		return s
	}
	var b strings.Builder
	last := 0
	for _, l := range locs {
		before := l[0] > 0 && (isDigitByte(s[l[0]-1]) || ((s[l[0]-1] == ',' || s[l[0]-1] == '.') && l[0] > 1 && isDigitByte(s[l[0]-2])))
		after := l[1] < len(s) && isDigitByte(s[l[1]])
		if before || after {
			continue
		}
		b.WriteString(s[last:l[0]])
		for _, r := range s[l[0]:l[1]] {
			if unicode.IsDigit(r) {
				b.WriteRune(r)
			}
		}
		last = l[1]
	}
	b.WriteString(s[last:])
	return b.String()
}

func isDigitByte(c byte) bool { return c >= '0' && c <= '9' }

// tokenize режет нормализованный текст на слова, числа и дефисы диапазонов.
// Число — цифры с одной десятичной запятой или точкой между цифрами:
// «2,5» и «2.5» — одно и то же число, а «45, 54» — два.
func tokenize(s string) []token {
	rs := []rune(s)
	var out []token
	for i := 0; i < len(rs); {
		r := rs[i]
		switch {
		case unicode.IsDigit(r):
			j := i
			for j < len(rs) && unicode.IsDigit(rs[j]) {
				j++
			}
			if j+1 < len(rs) && (rs[j] == ',' || rs[j] == '.') && unicode.IsDigit(rs[j+1]) {
				j++
				for j < len(rs) && unicode.IsDigit(rs[j]) {
					j++
				}
			}
			text := strings.ReplaceAll(string(rs[i:j]), ",", ".")
			v, _ := strconv.ParseFloat(text, 64)
			out = append(out, token{text: text, num: true, value: v})
			i = j
		case unicode.IsLetter(r):
			j := i
			for j < len(rs) && unicode.IsLetter(rs[j]) {
				j++
			}
			w := string(rs[i:j])
			// Число словами — слово со значением: в формах оно сравнивается
			// и как число («2 вида» = «два вида»), и как слово.
			out = append(out, token{text: w, value: wordNumbers[w]})
			i = j
		case r == '-' || r == '–' || r == '—' || r == '‒':
			out = append(out, token{text: "-", dash: true})
			i++
		default:
			i++
		}
	}
	return out
}

// parse разбирает ответ: токены для форм и числа с множителями.
func parse(s string) parsed {
	norm := normalize(s)
	all := tokenize(norm)
	p := parsed{norm: norm}
	// Числа: цифрами и словами; множитель после числа (или диапазона)
	// умножает и его, и начало диапазона.
	type numAt struct {
		i int
		v float64
	}
	var nums []numAt
	for i, t := range all {
		switch {
		case t.num:
			nums = append(nums, numAt{i, t.value})
		case !t.dash && t.value != 0:
			nums = append(nums, numAt{i, t.value})
		}
	}
	vals := make([]float64, len(nums))
	for k, n := range nums {
		vals[k] = n.v
	}
	for k, n := range nums {
		if n.i+1 >= len(all) {
			continue
		}
		m := multiplier(all[n.i+1].text)
		if m == 1 || all[n.i+1].num {
			continue
		}
		vals[k] = n.v * m
		// «2–3 тыс.»: начало диапазона — тоже тысячи.
		if k > 0 && nums[k-1].i == n.i-2 && all[n.i-1].dash {
			vals[k-1] = nums[k-1].v * m
		}
	}
	p.values = vals
	for _, t := range all {
		if !t.dash {
			p.toks = append(p.toks, t)
		}
	}
	return p
}

// multiplier — «тыс.», «тысяч», «млн», «миллиона»; 1 — не множитель.
func multiplier(w string) float64 {
	switch {
	case strings.HasPrefix(w, "тыс"):
		return 1e3
	case w == "млн" || strings.HasPrefix(w, "миллион"):
		return 1e6
	case w == "млрд" || strings.HasPrefix(w, "миллиард"):
		return 1e9
	}
	return 1
}

// wordNumbers — числа словами во всех падежах, что встречаются в ответах:
// «три-четыре мыши», «два вида», «тринадцать часов», «в двух видах».
var wordNumbers = func() map[string]float64 {
	m := map[string]float64{"полтора": 1.5, "полторы": 1.5, "полутора": 1.5}
	add := func(v float64, forms ...string) {
		for _, f := range forms {
			m[f] = v
		}
	}
	add(1, "один", "одна", "одно", "одного", "одной", "одному", "одним", "одну")
	add(2, "два", "две", "двух", "двум", "двумя")
	add(3, "три", "трех", "трем", "тремя")
	add(4, "четыре", "четырех", "четырем", "четырьмя")
	add(5, "пять", "пяти", "пятью")
	add(6, "шесть", "шести", "шестью")
	add(7, "семь", "семи")
	add(8, "восемь", "восьми", "восемью")
	add(9, "девять", "девяти", "девятью")
	add(10, "десять", "десяти", "десятью")
	for i, stem := range []string{"одиннадцат", "двенадцат", "тринадцат", "четырнадцат", "пятнадцат",
		"шестнадцат", "семнадцат", "восемнадцат", "девятнадцат", "двадцат", "тридцат"} {
		v := float64(11 + i)
		switch stem {
		case "двадцат":
			v = 20
		case "тридцат":
			v = 30
		}
		add(v, stem+"ь", stem+"и", stem+"ью")
	}
	return m
}()

// clip — первые n символов строки в одну строку, с многоточием.
func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
