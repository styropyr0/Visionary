package uptimecore

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// Ping checks send ICMP echo requests without raw-socket privileges: datagram
// sockets on Linux and macOS (Linux needs net.ipv4.ping_group_range, which Docker
// allows), a raw socket as fallback, the ICMP helper API on Windows. Where none
// works, checks wait without recording results and the dashboard says why.

// PingConfig is the settings of a ping check. The check fails when more
// than MaxLossPct of the packets are lost.
type PingConfig struct {
	Host       string `json:"host"`
	Packets    int    `json:"packets"`
	MaxLossPct int    `json:"max_loss_pct"`
}

const (
	PingGapMs      = 200
	pingMaxPackets = 20
)

var errPingUnavailable = errors.New("ICMP ping is not available on this server")

func (p *PingConfig) Normalize() error {
	h, err := NormalizeHost(p.Host)
	if err != nil {
		return err
	}
	p.Host = h
	if p.Packets == 0 {
		p.Packets = 4
	}
	if p.Packets < 1 || p.Packets > pingMaxPackets {
		return fmt.Errorf("packets must be between 1 and %d", pingMaxPackets)
	}
	if p.MaxLossPct < 0 || p.MaxLossPct > 99 {
		return errors.New("allowed loss must be between 0 and 99%")
	}
	return nil
}

type PingDetail struct {
	Address  string     `json:"address"`
	Sent     int        `json:"sent"`
	Received int        `json:"received"`
	LossPct  float64    `json:"loss_pct"`
	MinMs    *float64   `json:"min_ms"`
	AvgMs    *float64   `json:"avg_ms"`
	MaxMs    *float64   `json:"max_ms"`
	RTTs     []*float64 `json:"rtts"`
	Method   string     `json:"method"`
}

type PingState struct {
	Available bool   `json:"available"`
	Method    string `json:"method,omitempty"`
	Error     string `json:"error,omitempty"`
	Help      string `json:"help,omitempty"`
}

var pingProbe struct {
	sync.Mutex
	at    time.Time
	state PingState
}

// PingStatus says whether ping checks can run here, tested at most once a
// minute.
func PingStatus() PingState {
	pingProbe.Lock()
	defer pingProbe.Unlock()
	if time.Since(pingProbe.at) < time.Minute {
		return pingProbe.state
	}
	method, err := pingOpen()
	st := PingState{Available: err == nil, Method: method}
	if err != nil {
		st.Error, st.Help = errPingUnavailable.Error()+": "+err.Error(), pingHelp
	}
	pingProbe.at, pingProbe.state = time.Now(), st
	return st
}

func probePing(ctx context.Context, c Check, allowPrivate func() bool) (res Result) {
	start := time.Now()
	p := c.Ping
	detail := PingDetail{Sent: p.Packets, RTTs: []*float64{}}
	defer func() {
		res.CreatedAt = start.UTC().Format(TimeLayout)
		if detail.AvgMs != nil {
			res.DurationMs = math.Round(*detail.AvgMs*1000) / 1000
		} else {
			res.DurationMs = msSince(start)
		}
		res.Detail = detail
	}()
	fail := func(err error) Result {
		res.OK, res.Error, res.blocked = false, ProbeError(err, c.TimeoutMs), errors.Is(err, ErrBlockedAddress)
		return res
	}
	st := PingStatus()
	if !st.Available {
		res.Skip = st.Error
		return res
	}
	detail.Method = st.Method
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	addr, err := resolveGuarded(rctx, allowPrivate, p.Host)
	cancel()
	if err != nil {
		return fail(err)
	}
	detail.Address = addr.String()
	rtts, err := pingHost(ctx, addr, p.Packets, time.Duration(c.TimeoutMs)*time.Millisecond)
	if err != nil {
		return fail(err)
	}
	detail.RTTs = rtts
	var sum float64
	for _, r := range rtts {
		if r == nil {
			continue
		}
		detail.Received++
		sum += *r
		if detail.MinMs == nil || *r < *detail.MinMs {
			detail.MinMs = msPtr(*r)
		}
		if detail.MaxMs == nil || *r > *detail.MaxMs {
			detail.MaxMs = msPtr(*r)
		}
	}
	detail.LossPct = math.Round(1000*float64(p.Packets-detail.Received)/float64(p.Packets)) / 10
	if detail.Received > 0 {
		detail.AvgMs = msPtr(math.Round(sum/float64(detail.Received)*1000) / 1000)
	}
	if detail.Received == 0 {
		return fail(fmt.Errorf("no reply to %d packets", p.Packets))
	}
	if detail.LossPct > float64(p.MaxLossPct) {
		return fail(fmt.Errorf("%g%% packet loss (allowed %d%%)", detail.LossPct, p.MaxLossPct))
	}
	res.OK = true
	res.Degraded = detail.Received < p.Packets || (c.DegradedMs > 0 && *detail.AvgMs > float64(c.DegradedMs))
	return res
}
