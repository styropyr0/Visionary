package analytics

import (
	"net/http"
	"sync"
)

// Restarting from the dashboard. Some settings are read once when the
// server starts; the values they had then are kept, so Settings → Server can
// say which saved changes still wait for a restart. An admin's restart asks
// the command that runs the server (cmd/visionary) to stop and start itself
// again; the server can't do it alone, since it doesn't own the listener.

type restartState struct {
	requested chan struct{}
	once      sync.Once
	mu        sync.Mutex
	started   map[string]string // restart-only values when the server started
}

var restartStates sync.Map // *Server -> *restartState

func (s *Server) restartState() *restartState {
	if v, ok := restartStates.Load(s); ok {
		return v.(*restartState)
	}
	v, _ := restartStates.LoadOrStore(s, &restartState{requested: make(chan struct{})})
	return v.(*restartState)
}

// RestartRequested is closed when an admin asks for a restart.
func (s *Server) RestartRequested() <-chan struct{} { return s.restartState().requested }

// noteStartConfig keeps the restart-only values in force from now on.
func (s *Server) noteStartConfig() {
	st := s.restartState()
	vals := map[string]string{}
	for _, d := range confDefs {
		if d.Restart {
			vals[d.Key] = s.conf(d.Key)
		}
	}
	st.mu.Lock()
	st.started = vals
	st.mu.Unlock()
}

// restartPending lists the restart-only settings whose saved value differs
// from the one the server started with.
func (s *Server) restartPending() []string {
	st := s.restartState()
	st.mu.Lock()
	defer st.mu.Unlock()
	out := []string{}
	if st.started == nil {
		return out
	}
	for _, d := range confDefs {
		if d.Restart && s.conf(d.Key) != st.started[d.Key] {
			out = append(out, d.Key)
		}
	}
	return out
}

// handleRestart asks the process to restart. The environment is checked
// first: the new process reads it again, and a bad value would leave the
// server down with nothing to bring it back.
func (s *Server) handleRestart(w http.ResponseWriter, r *http.Request) {
	if !s.require(w, r, "admin") {
		return
	}
	if _, err := LoadConfig(); err != nil {
		writeError(w, http.StatusConflict, "not restarting: the environment has an invalid value, so the server would not start again ("+err.Error()+")")
		return
	}
	s.audit(r, "server.restart", "", map[string]any{"pending": s.restartPending()})
	s.logger.Info("restart requested from the dashboard")
	writeJSON(w, http.StatusAccepted, map[string]any{"restarting": true})
	st := s.restartState()
	st.once.Do(func() { close(st.requested) })
}
