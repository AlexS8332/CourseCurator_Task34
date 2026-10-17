package ps

import (
	"context"
	"strings"
	"testing"
)

func TestCommandQuotes(t *testing.T) {
	cmd := Command(`D:\course`, "doctor", "05", "it's")
	want := `[Console]::OutputEncoding=[Text.Encoding]::UTF8; . 'D:\course\scripts\aitr.ps1'; aitr 'doctor' '05' 'it''s'`
	if cmd != want {
		t.Fatalf("команда:\n%s\nждал:\n%s", cmd, want)
	}
}

func TestFake(t *testing.T) {
	f := Fake{"doctor 05": "  ok    stack = go\n"}
	out, err := f.Run(context.Background(), "doctor", "05")
	if err != nil || !strings.Contains(out, "stack = go") {
		t.Fatalf("%q %v", out, err)
	}
	if _, err := f.Run(context.Background(), "sync"); err == nil {
		t.Fatal("неизвестная команда должна быть ошибкой")
	}
	r := &Recorder{Inner: f}
	r.Run(context.Background(), "doctor", "05")
	if len(r.Calls) != 1 || r.Calls[0] != "doctor 05" {
		t.Fatalf("вызовы: %v", r.Calls)
	}
}

func TestAitrNoArgs(t *testing.T) {
	if _, err := Aitr(t.TempDir()).Run(context.Background()); err == nil {
		t.Fatal("без команды — ошибка, PowerShell не запускается")
	}
}
