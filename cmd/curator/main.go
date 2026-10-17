// curator — куратор курса: индекс документации, ассистент /help,
// MCP-сервер course, окно, стенд испытаний и снимок модуля в задание.
//
// Подкоманды регистрируются через init() в своих файлах (index.go,
// mcp.go, snapshot.go, help.go, serve.go, bench.go): main знает только
// карту команд и общие помощники — корень курса и каталог данных.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"

	"aitr/34-files-agent/internal/course"
)

// commands — подкоманды по имени; usage — строка справки каждой.
var (
	commands = map[string]func(ctx context.Context, args []string) error{}
	usage    = map[string]string{}
)

// register — регистрация подкоманды из init().
func register(name, help string, fn func(ctx context.Context, args []string) error) {
	commands[name] = fn
	usage[name] = help
}

func main() {
	if len(os.Args) < 2 || os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help" && len(os.Args) == 2 {
		printUsage()
		os.Exit(2)
	}
	name := os.Args[1]
	fn, ok := commands[name]
	if !ok {
		fmt.Fprintf(os.Stderr, "curator: неизвестная команда %q\n\n", name)
		printUsage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := fn(ctx, os.Args[2:]); err != nil {
		fmt.Fprintf(os.Stderr, "curator %s: %v\n", name, err)
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "curator — куратор курса. Команды:")
	names := make([]string, 0, len(usage))
	for n := range usage {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-10s %s\n", n, usage[n])
	}
}

// openRoot — корень курса по флагу -root (пусто — от текущего каталога
// вверх).
func openRoot(root string) (*course.Root, error) {
	r, err := course.Open(root)
	if err != nil {
		return nil, err
	}
	return r, nil
}

// EnvData — переменная с каталогом данных куратора.
const EnvData = "CURATOR_DATA"

// dataDir — где лежат course.db, schema/ и runs/: CURATOR_DATA, иначе
// <корень>/tools/curator/data (вне git). Снимок в задании работает с тем
// же каталогом корня: данные не копируются.
func dataDir(root *course.Root, flag string) string {
	if flag != "" {
		return flag
	}
	if v := strings.TrimSpace(os.Getenv(EnvData)); v != "" {
		return v
	}
	return filepath.Join(root.Dir, "tools", "curator", "data")
}

// dbPath — файл базы документации.
func dbPath(data string) string { return filepath.Join(data, "course.db") }
