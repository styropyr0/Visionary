package uptimecore

import (
	"context"
	"math"
	"time"

	"github.com/chromedp/cdproto/network"
)

// Each run keeps its requests for a waterfall and HAR download. URLs lose
// credentials and secret-looking query values; step screenshots are capped per run.

const (
	MaxRequests     = 300
	MaxStepShot     = 60 << 10
	StepShotsPerRun = 1 << 20
)

type NetRequest struct {
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

	start, end float64
}

func (st *runState) request(id network.RequestID) *NetRequest {
	if r, ok := st.reqs[id]; ok {
		return r
	}
	if len(st.order) >= MaxRequests {
		return &NetRequest{}
	}
	r := &NetRequest{}
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
		prev.Status, prev.MIME, prev.end = ev.RedirectResponse.Status, ev.RedirectResponse.MimeType, ev.Timestamp.Float64()
		delete(st.reqs, ev.RequestID)
	}
	r := st.request(ev.RequestID)
	r.start, r.Type = ev.Timestamp.Float64(), string(ev.Type)
	if ev.Request != nil {
		r.URL, r.Method = clip(st.mask(RedactURL(ev.Request.URL)), browserMaxEntryLen), ev.Request.Method
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
func (st *runState) requests(start, end time.Time) []NetRequest {
	st.mu.Lock()
	defer st.mu.Unlock()
	out := make([]NetRequest, 0, len(st.order))
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
	if st.shotBytes >= StepShotsPerRun {
		return nil
	}
	b := shoot(ctx, 0.5/scale, MaxStepShot, []int64{50, 35, 25})
	st.shotBytes += len(b)
	return b
}
