package files

import (
	"sort"
	"sync"
)

// Sessions — прогоны файлового агента по ID: окно (POST /api/files/plan →
// /api/files/approve по run_id) и CLI держат Run между этапами здесь.
// Состояние этапа живёт на сервере, а не в реплике человека: согласие
// читает код по словам (consent.Decided), и применить можно только тот
// план, который был показан. Безопасен для нескольких горутин.
type Sessions struct {
	mu   sync.Mutex
	runs map[string]*Run
}

// NewSessions — пустое хранилище.
func NewSessions() *Sessions { return &Sessions{runs: map[string]*Run{}} }

// Put кладёт прогон (по r.ID; повтор ID — замена).
func (s *Sessions) Put(r *Run) {
	if s == nil || r == nil || r.ID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runs == nil {
		s.runs = map[string]*Run{}
	}
	s.runs[r.ID] = r
}

// Get — прогон по ID.
func (s *Sessions) Get(id string) (*Run, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	return r, ok
}

// Remove убирает прогон из хранилища (worktree на диске не трогает).
func (s *Sessions) Remove(id string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.runs, id)
}

// List — прогоны по порядку ID (ID начинается со времени создания, так
// что порядок — хронологический).
func (s *Sessions) List() []*Run {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Run, 0, len(s.runs))
	for _, r := range s.runs {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Len — сколько прогонов в хранилище.
func (s *Sessions) Len() int {
	if s == nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.runs)
}
