package gitx

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DefaultDeny — что ассистенту читать нельзя: служебный каталог git,
// файлы с ключами (.env, .env.local, *.local) и сгенерированные
// INDEX.md / docs/registry.json (их источник — assignment.json, и модель
// должна читать его, а не слепок).
var DefaultDeny = []string{".git", ".env", ".env.*", "*.local", "INDEX.md", "docs/registry.json"}

// Guard — ограда путей: всё, что читает инструмент, лежит внутри Root, не
// уходит наружу по символической ссылке и не попадает под Deny.
//
// Образец Deny без «/» сравнивается с каждым элементом пути (так «.git»
// закрывает и assignments/05-x/.git), образец с «/» — с относительным
// путём целиком; синтаксис — path.Match.
type Guard struct {
	Root string
	Deny []string
}

// ErrDenied — путь запрещён оградой.
var ErrDenied = errors.New("путь запрещён")

// Resolve превращает относительный путь (с любыми разделителями) в
// абсолютный внутри Root. Абсолютный путь внутри Root тоже принимается.
// Файл может не существовать — это проверяет вызывающий; но существующая
// часть пути не должна вести наружу через символическую ссылку.
func (g Guard) Resolve(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", errors.New("пустой путь")
	}
	root, err := filepath.Abs(g.Root)
	if err != nil {
		return "", err
	}
	var abs string
	if filepath.IsAbs(rel) {
		abs = filepath.Clean(rel)
	} else {
		abs = filepath.Join(root, filepath.FromSlash(rel))
	}
	inside, err := filepath.Rel(root, abs)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %s вне корня", ErrDenied, rel)
	}
	slash := filepath.ToSlash(inside)
	deny := g.Deny
	if deny == nil {
		deny = DefaultDeny
	}
	for _, pat := range deny {
		if g.matches(pat, slash) {
			return "", fmt.Errorf("%w: %s (правило %s)", ErrDenied, slash, pat)
		}
	}
	if err := noEscape(root, abs); err != nil {
		return "", err
	}
	return abs, nil
}

func (g Guard) matches(pat, rel string) bool {
	if rel == "." {
		return false
	}
	if strings.Contains(pat, "/") {
		ok, _ := path.Match(pat, rel)
		return ok
	}
	for _, part := range strings.Split(rel, "/") {
		if ok, _ := path.Match(pat, part); ok {
			return true
		}
	}
	return false
}

// noEscape — существующая часть пути после раскрытия символических ссылок
// остаётся внутри корня.
func noEscape(root, abs string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	p := abs
	for {
		if _, err := os.Lstat(p); err == nil {
			break
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return nil
	}
	if rel, err := filepath.Rel(realRoot, real); err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("%w: %s ведёт за пределы корня", ErrDenied, filepath.Base(abs))
	}
	return nil
}
