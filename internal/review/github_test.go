package review

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeGitHub — httptest-сервер с путями REST GitHub для одного PR.
func fakeGitHub(t *testing.T) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var posted []map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("/repos/AlexS8332/AITrenning/pulls/7", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", 405)
			return
		}
		if r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("X-GitHub-Api-Version") == "" {
			http.Error(w, `{"message":"Bad credentials"}`, 401)
			return
		}
		if strings.Contains(r.Header.Get("Accept"), "application/vnd.github.diff") {
			w.Header().Set("Content-Type", "application/vnd.github.v3.diff; charset=utf-8")
			io.WriteString(w, sampleDiff)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"number":7,"title":"P4: publish.ps1","body":"описание","html_url":"https://github.com/AlexS8332/AITrenning/pull/7",
			"base":{"ref":"main"},"head":{"ref":"feat/publish","sha":"abc123"}}`)
	})
	mux.HandleFunc("/repos/AlexS8332/AITrenning/pulls/7/reviews", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"id":1,"body":"человек: ок"},{"id":2,"body":"ревью\n\n<!-- curator-review:abc123 -->"}]`)
		case http.MethodPost:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, err.Error(), 400)
				return
			}
			posted = append(posted, body)
			w.WriteHeader(200)
			io.WriteString(w, `{"id":3}`)
		default:
			http.Error(w, "method", 405)
		}
	})
	mux.HandleFunc("/repos/AlexS8332/AITrenning/pulls/404", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"Not Found","documentation_url":"x"}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, &posted
}

func TestGitHubClient(t *testing.T) {
	srv, posted := fakeGitHub(t)
	gh := NewGitHub(srv.URL, "tok")
	ctx := context.Background()

	pr, err := gh.PR(ctx, "AlexS8332", "AITrenning", 7)
	if err != nil {
		t.Fatal(err)
	}
	if pr.Number != 7 || pr.Title != "P4: publish.ps1" || pr.Body != "описание" || pr.Base != "main" || pr.Head != "feat/publish" || pr.HeadSHA != "abc123" || !strings.HasSuffix(pr.HTMLURL, "/pull/7") {
		t.Errorf("PR: %+v", pr)
	}

	diff, err := gh.Diff(ctx, "AlexS8332", "AITrenning", 7)
	if err != nil {
		t.Fatal(err)
	}
	if diff != sampleDiff {
		t.Errorf("Diff: %q", clip(diff, 80))
	}

	reviews, err := gh.Reviews(ctx, "AlexS8332", "AITrenning", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 || reviews[1].ID != 2 {
		t.Errorf("Reviews: %+v", reviews)
	}
	if !HasMarker(reviews, "abc123") || HasMarker(reviews, "def456") {
		t.Error("HasMarker")
	}
	if Marker("abc") != "<!-- curator-review:abc -->" {
		t.Error("Marker")
	}

	err = gh.PostReview(ctx, "AlexS8332", "AITrenning", 7, "тело "+Marker("abc123"),
		[]Comment{{Path: "scripts/publish.ps1", Line: 2, Body: "push --force"}}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(*posted) != 1 {
		t.Fatalf("опубликовано %d", len(*posted))
	}
	p := (*posted)[0]
	if p["event"] != "COMMENT" || !strings.Contains(p["body"].(string), Marker("abc123")) {
		t.Errorf("тело: %+v", p)
	}
	cs, _ := p["comments"].([]any)
	if len(cs) != 1 {
		t.Fatalf("комментарии: %+v", p["comments"])
	}
	c := cs[0].(map[string]any)
	if c["path"] != "scripts/publish.ps1" || c["line"] != float64(2) || c["side"] != "RIGHT" || c["body"] != "push --force" {
		t.Errorf("комментарий: %+v", c)
	}

	// Ошибка API — со статусом и message.
	if _, err := gh.PR(ctx, "AlexS8332", "AITrenning", 404); err == nil || !strings.Contains(err.Error(), "404") || !strings.Contains(err.Error(), "Not Found") {
		t.Errorf("ошибка 404: %v", err)
	}
	// Без токена — 401.
	if _, err := NewGitHub(srv.URL, "").PR(ctx, "AlexS8332", "AITrenning", 7); err == nil || !strings.Contains(err.Error(), "Bad credentials") {
		t.Errorf("без токена: %v", err)
	}

	// FromPR собирает вход из PR и diff.
	in, err := FromPR(ctx, gh, "AlexS8332", "AITrenning", 7)
	if err != nil {
		t.Fatal(err)
	}
	if in.Number != 7 || in.Title != "P4: publish.ps1" || in.Head != "abc123" || in.Base != "main" || in.Range != "main...feat/publish" || len(in.Files) != 5 || in.Diff != sampleDiff {
		t.Errorf("FromPR: %+v", in)
	}
	if _, err := FromPR(ctx, gh, "AlexS8332", "AITrenning", 0); err == nil {
		t.Error("PR 0 — ошибка")
	}
}

func TestSplitRepo(t *testing.T) {
	o, r, err := SplitRepo("AlexS8332/AITrenning")
	if err != nil || o != "AlexS8332" || r != "AITrenning" {
		t.Errorf("%q %q %v", o, r, err)
	}
	for _, bad := range []string{"", "x", "a/b/c", "/b", "a/"} {
		if _, _, err := SplitRepo(bad); err == nil {
			t.Errorf("%q принят", bad)
		}
	}
}
