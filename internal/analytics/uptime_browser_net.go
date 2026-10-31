package analytics

import (
	"context"
	"encoding/base64"
	"math"
	"strconv"
	"time"

	"github.com/chromedp/cdproto/network"
)

// Each run keeps its requests for a waterfall and a HAR download: what was
// asked, how it answered, when and for how long. URLs lose credentials and
// secret-looking query values, and secrets are masked, as elsewhere.
// Optional step screenshots are small and capped per run, and only the
// latest runs of a check keep theirs; passing runs keep their requests for
// longer, failed ones for as long as the run is kept.

const (
	browserMaxRequests  = 300
	browserStepShot     = 60 << 10
	browserStepShotsRun = 1 << 20
	browserKeepStepRuns = 24
	browserKeepReqRuns  = 200
)

// netRequest is one request of a run.
type netRequest struct {
	URL        string  `json:"url"`
	Method     string  `json:"method"`
	Status     int64   `json:"status,omitempty"`
	Type       string  `json:"type,omitempty"`
	MIME       string  `json:"mime,omitempty"`
	StartMs    float64 `json:"start_ms"`
	DurationMs float64 `json:"duration_ms"`
	Size       int64   `json:"size,omitempty"`
	Failed     bool    `json:"failed,omitempty"`
	Error      string  `json:"error,omitempty"`

	start, end float64 // monotonic seconds
}

// request is the entry of id, made on whichever event comes first.
func (st *runState) request(id network.RequestID) *netRequest {
	if r, ok := st.reqs[id]; ok {
		return r
	}
	if len(st.order) >= browserMaxRequests {
		return &netRequest{}
	}
	r := &netRequest{}
	st.reqs[id] = r
	st.order = append(st.order, r)
	return r
}

func (st *runState) netSent(ev *network.EventRequestWillBeSent) {
	st.mu.Lock()
	defer st.mu.Unlock()
	if st.wallBase == 0 && ev.WallTime > 0 {
		st.wallBase = ev.WallTime.Float64() - ev.Timestamp.Float64()
	}
	if prev, ok := st.reqs[ev.RequestID]; ok && ev.RedirectResponse != nil {
		// A redirect ends the previous hop; the next one is a new entry.
		prev.Status, prev.MIME, prev.end = ev.RedirectResponse.Status, ev.RedirectResponse.MimeType, ev.Timestamp.Float64()
		delete(st.reqs, ev.RequestID)
	}
	r := st.request(ev.RequestID)
	r.start, r.Type = ev.Timestamp.Float64(), string(ev.Type)
	if ev.Request != nil {
		r.URL, r.Method = clip(st.mask(redactURL(ev.Request.URL)), browserMaxEntryLen), ev.Request.Method
	}
}

func (st *runState) netResponse(ev *network.EventResponseReceived) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.request(ev.RequestID)
	if ev.Response != nil {
		r.Status, r.MIME = ev.Response.Status, ev.Response.MimeType
	}
	if r.Type == "" {
		r.Type = string(ev.Type)
	}
}

func (st *runState) netFinished(ev *network.EventLoadingFinished) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.request(ev.RequestID)
	r.end, r.Size = ev.Timestamp.Float64(), int64(ev.EncodedDataLength)
}

func (st *runState) netFailed(ev *network.EventLoadingFailed, reason string) {
	st.mu.Lock()
	defer st.mu.Unlock()
	r := st.request(ev.RequestID)
	r.end, r.Failed, r.Error = ev.Timestamp.Float64(), true, clip(st.mask(reason), browserMaxEntryLen)
}

// requests are the run's requests, timed from start; one still open when the
// run ended lasts until then.
func (st *runState) requests(start, end time.Time) []netRequest {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]netRequest, 0, len(st.order))
	t0 := float64(start.UnixMicro()) / 1e6
	last := float64(end.UnixMicro())/1e6 - st.wallBase
	ms := func(v float64) float64 { return math.Round(v*10000) / 10 }
	for _, r := range st.order {
		if r.URL == "" || r.start == 0 {
			continue
		}
		e := r.end
		if e < r.start {
			e = math.Max(last, r.start)
		}
		v := *r
		v.StartMs, v.DurationMs = ms(math.Max(0, r.start+st.wallBase-t0)), ms(e-r.start)
		out = append(out, v)
	}
	return out
}

// stepShot is a small screenshot after a step, or nil once the run's shots
// reach their cap.
func (st *runState) stepShot(ctx context.Context, scale float64) []byte {
	if st.shotBytes >= browserStepShotsRun {
		return nil
	}
	b := shoot(ctx, 0.5/scale, browserStepShot, []int64{50, 35, 25})
	st.shotBytes += len(b)
	return b
}

func encodeShots(shots [][]byte) []string {
	out := make([]string, len(shots))
	for i, b := range shots {
		if b != nil {
			out[i] = base64.StdEncoding.EncodeToString(b)
		}
	}
	return out
}

// pruneRunDetail drops the step screenshots of all but a check's latest
// runs, and the requests of all but its latest passing runs.
func (s *Server) pruneRunDetail(ctx context.Context, checkID int64) error {
	for _, p := range []struct {
		col, where string
		keep       int
	}{{"step_shots", "", browserKeepStepRuns}, {"requests", " AND ok = 1", browserKeepReqRuns}} {
		cond := `check_id = ? AND ` + p.col + ` IS NOT NULL` + p.where
		rows, err := s.db.Query(ctx, `SELECT created_at FROM uptime_browser_results WHERE `+cond+`
ORDER BY created_at DESC LIMIT 1 OFFSET `+strconv.Itoa(p.keep), checkID)
		if err != nil {
			return err
		}
		if len(rows.Rows) == 0 {
			continue
		}
		if err := s.db.Exec(ctx, `UPDATE uptime_browser_results SET `+p.col+` = NULL WHERE `+cond+` AND created_at <= ?`, checkID, toString(rows.Rows[0][0])); err != nil {
			return err
		}
	}
	return nil
}
