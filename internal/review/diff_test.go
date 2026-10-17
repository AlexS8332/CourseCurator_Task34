package review

import (
	"strings"
	"testing"
)

const sampleDiff = `diff --git a/tools/edgerec/report.go b/tools/edgerec/report.go
index 1111111..2222222 100644
--- a/tools/edgerec/report.go
+++ b/tools/edgerec/report.go
@@ -34,9 +34,7 @@ func (r *Report) Write(dir string) error {
 	if err != nil {
 		return err
 	}
-	if err := os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644); err != nil {
-		return err
-	}
+	_ = os.WriteFile(filepath.Join(dir, "report.json"), b, 0o644)
 	return os.WriteFile(filepath.Join(dir, "report.md"), []byte(r.Markdown()), 0o644)
 }

@@ -60,3 +58,4 @@ func (r *Report) Markdown() string {
 	a
 	b
 	c
+	d
diff --git a/scripts/publish.ps1 b/scripts/publish.ps1
new file mode 100644
index 0000000..3333333
--- /dev/null
+++ b/scripts/publish.ps1
@@ -0,0 +1,3 @@
+# publish
+git push --force
+Write-Host $env:KEY
\ No newline at end of file
diff --git a/docs/old.md b/docs/old.md
deleted file mode 100644
index 4444444..0000000
--- a/docs/old.md
+++ /dev/null
@@ -1,2 +0,0 @@
-# Старое
-текст
diff --git a/docs/notes/a.md b/docs/notes/b.md
similarity index 90%
rename from docs/notes/a.md
rename to docs/notes/b.md
index 5555555..6666666 100644
--- a/docs/notes/a.md
+++ b/docs/notes/b.md
@@ -1,2 +1,2 @@
 # Конспект
-старая строка
+новая строка
diff --git a/img/sheet.png b/img/sheet.png
index 7777777..8888888 100644
Binary files a/img/sheet.png and b/img/sheet.png differ
`

func TestParseUnified(t *testing.T) {
	files, err := ParseUnified(sampleDiff)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 5 {
		t.Fatalf("файлов %d, ждал 5", len(files))
	}

	// Изменённый файл с двумя hunk'ами.
	f := files[0]
	if f.Path != "tools/edgerec/report.go" || f.Status != StatusModified || f.OldPath != "" || f.Binary {
		t.Errorf("report.go: %+v", f)
	}
	if len(f.Hunks) != 2 || f.Added != 2 || f.Removed != 3 {
		t.Errorf("hunks %d, +%d −%d", len(f.Hunks), f.Added, f.Removed)
	}
	h := f.Hunks[0]
	if h.OldStart != 34 || h.OldLines != 9 || h.NewStart != 34 || h.NewLines != 7 || !strings.HasPrefix(h.Header, "func (r *Report) Write") {
		t.Errorf("заголовок hunk'а: %+v", h)
	}
	if len(h.Lines) != 10 {
		t.Fatalf("строк в hunk'е %d", len(h.Lines))
	}
	// Нумерация: контекст 34–36 (old=new), удалённые 37–39 old, добавленная 37 new, контекст 38–40.
	if l := h.Lines[0]; l.Kind != ' ' || l.Old != 34 || l.New != 34 {
		t.Errorf("первая строка: %+v", l)
	}
	if l := h.Lines[3]; l.Kind != '-' || l.Old != 37 || l.New != 0 {
		t.Errorf("удалённая: %+v", l)
	}
	if l := h.Lines[6]; l.Kind != '+' || l.Old != 0 || l.New != 37 || !strings.Contains(l.Text, "_ = os.WriteFile") {
		t.Errorf("добавленная: %+v", l)
	}
	if l := h.Lines[7]; l.Kind != ' ' || l.Old != 40 || l.New != 38 {
		t.Errorf("контекст после: %+v", l)
	}
	if l := h.Lines[9]; l.Kind != ' ' || l.Text != "" || l.New != 40 {
		t.Errorf("пустая контекстная строка: %+v", l)
	}
	if h2 := f.Hunks[1]; h2.NewStart != 58 || len(h2.Lines) != 4 || h2.Lines[3].New != 61 {
		t.Errorf("второй hunk: %+v", h2)
	}

	// Commentable / LineText.
	for _, n := range []int{34, 35, 36, 37, 38, 39, 40, 58, 59, 60, 61} {
		if !f.Commentable(n) {
			t.Errorf("строка %d должна быть комментируемой", n)
		}
	}
	for _, n := range []int{0, 1, 33, 41, 57, 62, 100} {
		if f.Commentable(n) {
			t.Errorf("строка %d не в diff", n)
		}
	}
	if txt, ok := f.LineText(37); !ok || !strings.Contains(txt, "_ = os.WriteFile") {
		t.Errorf("LineText(37) = %q, %v", txt, ok)
	}
	if txt, ok := f.LineText(61); !ok || txt != "\td" {
		t.Errorf("LineText(61) = %q, %v", txt, ok)
	}
	if got := f.Ranges(); got != "34–40, 58–61" {
		t.Errorf("Ranges: %q", got)
	}

	// Новый файл: /dev/null слева, «\ No newline» пропущен.
	a := files[1]
	if a.Path != "scripts/publish.ps1" || a.Status != StatusAdded || a.OldPath != "" || a.Added != 3 || a.Removed != 0 {
		t.Errorf("новый файл: %+v", a)
	}
	if txt, ok := a.LineText(2); !ok || txt != "git push --force" {
		t.Errorf("новый файл, строка 2: %q %v", txt, ok)
	}
	if a.Commentable(4) {
		t.Error("у нового файла из 3 строк строки 4 нет")
	}

	// Удалённый: Path — старый путь, комментировать нечего.
	d := files[2]
	if d.Path != "docs/old.md" || d.Status != StatusDeleted || d.Removed != 2 || d.Commentable(1) {
		t.Errorf("удалённый: %+v", d)
	}

	// Переименованный: Path новый, OldPath старый.
	r := files[3]
	if r.Path != "docs/notes/b.md" || r.OldPath != "docs/notes/a.md" || r.Status != StatusRenamed {
		t.Errorf("переименованный: %+v", r)
	}
	if !r.Commentable(1) || !r.Commentable(2) || r.Commentable(3) {
		t.Errorf("переименованный: комментируемые строки %v", r.CommentableLines())
	}

	// Бинарный: без hunk'ов.
	b := files[4]
	if b.Path != "img/sheet.png" || !b.Binary || len(b.Hunks) != 0 || b.Commentable(1) {
		t.Errorf("бинарный: %+v", b)
	}
}

func TestParseUnifiedPlainAndCRLF(t *testing.T) {
	// `diff -u` без «diff --git», с датами после табуляции и CRLF.
	plain := "--- a/x.txt\t2026-10-16 10:00:00\r\n+++ b/x.txt\t2026-10-16 10:01:00\r\n@@ -1 +1,2 @@\r\n one\r\n+two\r\n"
	files, err := ParseUnified(plain)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "x.txt" || files[0].Status != StatusModified {
		t.Fatalf("plain: %+v", files)
	}
	if txt, ok := files[0].LineText(2); !ok || txt != "two" {
		t.Errorf("строка 2: %q %v", txt, ok)
	}
	// Удалённая строка «-- x» внутри hunk'а не принимается за «---» заголовок.
	tricky := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,2 +1,1 @@\n--- not a header\n keep\n"
	files, err = ParseUnified(tricky)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Removed != 1 || files[0].Hunks[0].Lines[0].Text != "-- not a header" {
		t.Errorf("tricky: %+v", files)
	}
}

func TestParseUnifiedErrorsAndTruncation(t *testing.T) {
	if _, err := ParseUnified("@@ -1 +1 @@\n x\n"); err == nil {
		t.Error("hunk вне файла должен быть ошибкой")
	}
	if _, err := ParseUnified("diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,2 +1,2 @@\n a\n?b\n"); err == nil {
		t.Error("чужой префикс в теле hunk'а должен быть ошибкой")
	}
	files, err := ParseUnified("")
	if err != nil || len(files) != 0 {
		t.Errorf("пустой diff: %v %v", files, err)
	}
	// Обрезанный посреди hunk'а — не ошибка, разобранное остаётся.
	cut := "diff --git a/f b/f\n--- a/f\n+++ b/f\n@@ -1,5 +1,6 @@\n a\n+b\n"
	files, err = ParseUnified(cut)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || len(files[0].Hunks) != 1 || len(files[0].Hunks[0].Lines) != 2 || !files[0].Commentable(2) {
		t.Errorf("обрезанный: %+v", files)
	}
}

func TestSplitGitPaths(t *testing.T) {
	cases := map[string][2]string{
		"a/x.go b/x.go":                         {"x.go", "x.go"},
		"a/dir with space/x b/dir with space/x": {"dir with space/x", "dir with space/x"},
		"a/old.md b/new.md":                     {"old.md", "new.md"},
		`"a/\321\201.md" "b/\321\201.md"`:       {"с.md", "с.md"},
	}
	for in, want := range cases {
		a, b := splitGitPaths(in)
		if a != want[0] || b != want[1] {
			t.Errorf("%q → %q, %q; ждал %v", in, a, b, want)
		}
	}
	if NormPath(`.\a\tools\x.go`) != "tools/x.go" || NormPath("b/scripts/aitr.ps1") != "scripts/aitr.ps1" || NormPath("docs/x.md") != "docs/x.md" {
		t.Error("NormPath")
	}
}
