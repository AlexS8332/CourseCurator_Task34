package review

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GitHub — то, что ревью просит у GitHub. Реализация — REST-клиент
// NewGitHub; в тестах — httptest с теми же путями.
type GitHub interface {
	// PR — заголовок, описание, базовая и головная ветки, sha головы.
	PR(ctx context.Context, owner, repo string, n int) (PRInfo, error)
	// Diff — unified diff PR (Accept: application/vnd.github.diff).
	Diff(ctx context.Context, owner, repo string, n int) (string, error)
	// Reviews — опубликованные ревью PR (для дедупа по маркеру).
	Reviews(ctx context.Context, owner, repo string, n int) ([]PostedReview, error)
	// PostReview публикует ревью с событием event (COMMENT, APPROVE,
	// REQUEST_CHANGES) и inline-комментариями к строкам новой версии
	// (side RIGHT).
	PostReview(ctx context.Context, owner, repo string, n int, body string, comments []Comment, event string) error
}

// DefaultAPI — адрес REST GitHub по умолчанию.
const DefaultAPI = "https://api.github.com"

// EventComment — событие ревью «комментарий без вердикта»: ревьюер-бот
// не одобряет и не блокирует, решает человек.
const EventComment = "COMMENT"

// markerPrefix — начало маркера дедупа в теле ревью.
const markerPrefix = "<!-- curator-review:"

// Marker — маркер в теле ревью по sha головы PR: ревью с таким маркером
// уже есть — второй раз на тот же коммит не постим.
func Marker(sha string) string { return markerPrefix + sha + " -->" }

// HasMarker — есть ли среди опубликованных ревью маркер этого sha.
func HasMarker(reviews []PostedReview, sha string) bool {
	m := Marker(sha)
	for _, r := range reviews {
		if strings.Contains(r.Body, m) {
			return true
		}
	}
	return false
}

// client — REST-клиент GitHub с токеном (GITHUB_TOKEN в Actions, PAT
// локально).
type client struct {
	base  string
	token string
	http  *http.Client
}

// NewGitHub — клиент REST: baseURL пустой — api.github.com; token пустой —
// без авторизации (публичные репозитории, чтение).
func NewGitHub(baseURL, token string) GitHub {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = DefaultAPI
	}
	return &client{base: strings.TrimRight(baseURL, "/"), token: strings.TrimSpace(token),
		http: &http.Client{Timeout: 60 * time.Second}}
}

func (c *client) do(ctx context.Context, method, path, accept string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, rd)
	if err != nil {
		return err
	}
	if accept == "" {
		accept = "application/vnd.github+json"
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "aitr-curator")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 20<<20))
	if err != nil {
		return fmt.Errorf("GitHub %s %s: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("GitHub %s %s: %s: %s", method, path, resp.Status, clip(apiMessage(raw), 300))
	}
	switch v := out.(type) {
	case nil:
	case *string:
		*v = string(raw)
	default:
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("GitHub %s %s: ответ не разобран: %w", method, path, err)
		}
	}
	return nil
}

// apiMessage — поле message из JSON ошибки GitHub, иначе тело как есть.
func apiMessage(raw []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Message != "" {
		return e.Message
	}
	return strings.TrimSpace(string(raw))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func pullPath(owner, repo string, n int) string {
	return "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repo) + "/pulls/" + strconv.Itoa(n)
}

func (c *client) PR(ctx context.Context, owner, repo string, n int) (PRInfo, error) {
	var raw struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
		Base    struct {
			Ref string `json:"ref"`
		} `json:"base"`
		Head struct {
			Ref string `json:"ref"`
			SHA string `json:"sha"`
		} `json:"head"`
	}
	if err := c.do(ctx, http.MethodGet, pullPath(owner, repo, n), "", nil, &raw); err != nil {
		return PRInfo{}, err
	}
	return PRInfo{Number: raw.Number, Title: raw.Title, Body: raw.Body, Base: raw.Base.Ref,
		Head: raw.Head.Ref, HeadSHA: raw.Head.SHA, HTMLURL: raw.HTMLURL}, nil
}

func (c *client) Diff(ctx context.Context, owner, repo string, n int) (string, error) {
	var s string
	if err := c.do(ctx, http.MethodGet, pullPath(owner, repo, n), "application/vnd.github.diff", nil, &s); err != nil {
		return "", err
	}
	return s, nil
}

func (c *client) Reviews(ctx context.Context, owner, repo string, n int) ([]PostedReview, error) {
	var all []PostedReview
	for page := 1; page <= 10; page++ {
		var raw []struct {
			ID   int64  `json:"id"`
			Body string `json:"body"`
		}
		p := pullPath(owner, repo, n) + "/reviews?per_page=100&page=" + strconv.Itoa(page)
		if err := c.do(ctx, http.MethodGet, p, "", nil, &raw); err != nil {
			return nil, err
		}
		for _, r := range raw {
			all = append(all, PostedReview{ID: r.ID, Body: r.Body})
		}
		if len(raw) < 100 {
			break
		}
	}
	return all, nil
}

// reviewComment — inline-комментарий в теле запроса POST …/reviews.
type reviewComment struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Side string `json:"side"`
	Body string `json:"body"`
}

func (c *client) PostReview(ctx context.Context, owner, repo string, n int, body string, comments []Comment, event string) error {
	if event == "" {
		event = EventComment
	}
	req := struct {
		Body     string          `json:"body"`
		Event    string          `json:"event"`
		Comments []reviewComment `json:"comments,omitempty"`
	}{Body: body, Event: event}
	for _, cm := range comments {
		req.Comments = append(req.Comments, reviewComment{Path: cm.Path, Line: cm.Line, Side: "RIGHT", Body: cm.Body})
	}
	return c.do(ctx, http.MethodPost, pullPath(owner, repo, n)+"/reviews", "", req, nil)
}

// SplitRepo делит «owner/name» (GITHUB_REPOSITORY) на части.
func SplitRepo(s string) (owner, repo string, err error) {
	parts := strings.Split(strings.TrimSpace(s), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("репозиторий %q: нужно owner/name", s)
	}
	return parts[0], parts[1], nil
}
