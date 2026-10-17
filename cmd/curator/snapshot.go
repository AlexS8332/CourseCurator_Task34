package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func init() {
	register("snapshot", "NN [-root dir] [-force]  снимок tools/curator в корень assignments/NN-slug (модуль aitr/NN-slug)", runSnapshot)
}

// snapshotSkip — что в снимок не идёт: данные и бинарники (вне git и у
// корня), а также каталог самого снимка, если команду запустили из него.
var snapshotSkip = map[string]bool{"data": true, ".git": true}

// snapshotKeep — файлы задания, которые снимок не трогает: их завёл
// `aitr new` или автор (описание, метаданные, заметки, секреты).
var snapshotKeep = map[string]bool{
	"README.md": true, "assignment.json": true, "notes.md": true,
	".gitignore": true, ".env.local": true, ".env.local.example": true, ".env.example": true,
}

// runSnapshot — `curator snapshot NN`: копия дерева tools/curator корня
// в корень репозитория задания assignments/NN-slug без data/ и *.exe;
// строка module в go.mod и пути импортов переписываются на aitr/NN-slug,
// поэтому `go test ./...` в каталоге задания (его гоняет `aitr record`)
// проверяет снимок как обычный код задания. Файлы из snapshotKeep не
// перезаписываются, заготовка main.go шаблона удаляется. Прежний снимок
// заменяется только с -force.
func runSnapshot(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("snapshot", flag.ContinueOnError)
	rootFlag := fs.String("root", "", "корень курса")
	force := fs.Bool("force", false, "заменить прежний снимок")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("нужен номер или slug задания: curator snapshot 31")
	}
	root, err := openRoot(*rootFlag)
	if err != nil {
		return err
	}
	a, err := root.Assignment(fs.Arg(0))
	if err != nil {
		return err
	}
	src := filepath.Join(root.Dir, "tools", "curator")
	if _, err := os.Stat(filepath.Join(src, "go.mod")); err != nil {
		return fmt.Errorf("в %s нет go.mod — это не модуль куратора", src)
	}
	dst := a.Dir
	if _, err := os.Stat(filepath.Join(dst, "internal")); err == nil {
		if !*force {
			return fmt.Errorf("снимок уже есть: %s (замена — -force)", dst)
		}
		// Снимаем прежний снимок: верхние элементы дерева куратора,
		// кроме файлов задания; остальное в каталоге задания не трогаем.
		entries, err := os.ReadDir(src)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if snapshotSkip[e.Name()] || snapshotKeep[e.Name()] {
				continue
			}
			if err := os.RemoveAll(filepath.Join(dst, e.Name())); err != nil {
				return err
			}
		}
	}
	// Заготовка main.go из шаблона `aitr new` — лишний пакет main в корне.
	if raw, err := os.ReadFile(filepath.Join(dst, "main.go")); err == nil && strings.Contains(string(raw), "package main") {
		if err := os.Remove(filepath.Join(dst, "main.go")); err != nil {
			return err
		}
	}
	module := "aitr/" + a.DirName()
	n, err := Snapshot(src, dst, module)
	if err != nil {
		return err
	}
	fmt.Printf("снимок %s → %s: файлов %d, module %s\n", src, dst, n, module)
	return nil
}

var moduleLine = regexp.MustCompile(`(?m)^module\s+\S+`)

// Snapshot копирует дерево модуля src в dst (без data/, .git/, *.exe и
// *.db), переписывая module в go.mod и импорты «aitr/curator/…» в файлах
// .go на newModule. Возвращает число скопированных файлов.
func Snapshot(src, dst, newModule string) (int, error) {
	srcAbs, err := filepath.Abs(src)
	if err != nil {
		return 0, err
	}
	dstAbs, err := filepath.Abs(dst)
	if err != nil {
		return 0, err
	}
	oldModule, err := moduleOf(filepath.Join(srcAbs, "go.mod"))
	if err != nil {
		return 0, err
	}
	n := 0
	err = filepath.WalkDir(srcAbs, func(p string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(srcAbs, p)
		if rel == "." {
			return nil
		}
		if e.IsDir() {
			if snapshotSkip[e.Name()] || strings.HasPrefix(p, dstAbs) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dstAbs, rel), 0o755)
		}
		if ext := strings.ToLower(filepath.Ext(e.Name())); ext == ".exe" || ext == ".db" {
			return nil
		}
		if filepath.Dir(rel) == "." && snapshotKeep[e.Name()] {
			if _, err := os.Stat(filepath.Join(dstAbs, rel)); err == nil {
				return nil
			}
		}
		raw, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		switch {
		case rel == "go.mod":
			raw = moduleLine.ReplaceAll(raw, []byte("module "+newModule))
		case strings.HasSuffix(e.Name(), ".go"):
			raw = []byte(strings.ReplaceAll(string(raw), `"`+oldModule+`/`, `"`+newModule+`/`))
		}
		n++
		return os.WriteFile(filepath.Join(dstAbs, rel), raw, 0o644)
	})
	return n, err
}

// moduleOf — имя модуля из go.mod.
func moduleOf(gomod string) (string, error) {
	raw, err := os.ReadFile(gomod)
	if err != nil {
		return "", err
	}
	m := moduleLine.Find(raw)
	if m == nil {
		return "", fmt.Errorf("%s: нет строки module", gomod)
	}
	return strings.TrimSpace(strings.TrimPrefix(string(m), "module")), nil
}
