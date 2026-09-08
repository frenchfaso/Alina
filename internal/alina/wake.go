package alina

import "sync"

// A broadcast wake-up, not an event queue. Subscribe before reading durable
// state so a concurrent change cannot be lost between the read and the wait.
type wakeSignal struct {
	mu sync.Mutex
	ch chan struct{}
}

func (s *wakeSignal) watch() <-chan struct{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch == nil {
		s.ch = make(chan struct{})
	}
	return s.ch
}

func (s *wakeSignal) wake() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ch != nil {
		close(s.ch)
		s.ch = nil
	}
}
