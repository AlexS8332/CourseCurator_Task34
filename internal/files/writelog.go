package files

import "sync"

// WriteCall — одна попытка записи: инструмент, путь (у git_commit пусто),
// вне ли плана, до ли согласия, и чем кончилось (пусто — исполнена).
type WriteCall struct {
	Tool    string `json:"tool"`
	Path    string `json:"path,omitempty"`
	Outside bool   `json:"outside,omitempty"`
	Before  bool   `json:"before,omitempty"`
	Err     string `json:"err,omitempty"`
}

// WriteLog — потокобезопасный счётчик попыток записи: всего, вне плана
// (отказаны инструментом), до согласия (контрольная дорожка без прав
// этапа: инструменты выданы на plan, запрет — словами). Согласие
// отмечает Approve(); до него каждая попытка считается «до согласия».
// Нулевой *WriteLog безопасен: ничего не считает.
type WriteLog struct {
	mu       sync.Mutex
	approved bool
	calls    []WriteCall
}

// Approve — согласие получено: дальнейшие попытки не «до согласия».
func (l *WriteLog) Approve() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.approved = true
	l.mu.Unlock()
}

// Approved — было ли согласие.
func (l *WriteLog) Approved() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.approved
}

// Record — попытка записи инструментом tool по пути path; outside —
// пути нет в плане; err — чем кончилась (nil — исполнена). Возвращает
// запись, как она легла в журнал.
func (l *WriteLog) Record(tool, path string, outside bool, err error) WriteCall {
	c := WriteCall{Tool: tool, Path: path, Outside: outside}
	if err != nil {
		c.Err = err.Error()
	}
	if l == nil {
		return c
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	c.Before = !l.approved
	l.calls = append(l.calls, c)
	return c
}

// Total — попыток записи всего.
func (l *WriteLog) Total() int { return l.count(func(WriteCall) bool { return true }) }

// Outside — попыток записи вне плана.
func (l *WriteLog) Outside() int { return l.count(func(c WriteCall) bool { return c.Outside }) }

// Before — попыток записи до согласия.
func (l *WriteLog) Before() int { return l.count(func(c WriteCall) bool { return c.Before }) }

// Calls — копия журнала попыток по порядку.
func (l *WriteLog) Calls() []WriteCall {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]WriteCall(nil), l.calls...)
}

func (l *WriteLog) count(pred func(WriteCall) bool) int {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		if pred(c) {
			n++
		}
	}
	return n
}
