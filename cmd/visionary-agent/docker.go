package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// dockerCollector reads containers from the Docker Engine API on its unix
// socket; Podman serves the same API. Every host sample it takes one stats
// reading per running container (CPU and I/O rates are deltas between two
// readings); every report it inspects every container, and compares with the
// previous report to find restarts, OOM kills, exits and health changes.
type dockerCollector struct {
	a       *agent
	setting string // a socket path, or "" to find one
	sock    string // the socket in use; "" until one answers
	client  *http.Client

	prev    map[string]statsReading // by container ID, the last stats reading
	limits  map[string]ctrLimits    // by container ID, from the last inspect
	states  map[string]ctrState     // by container ID, at the last report
	samples []agentapi.ContainerSample
	risks   *riskTracker // nil when container risks are off
}

type statsReading struct {
	at                time.Time
	cpu               uint64 // total CPU time, ns
	periods, throttle uint64 // CFS periods, throttled periods
	rx, tx            uint64
	read, write       uint64
}

type ctrLimits struct {
	cpu   float64 // cores; 0 is no limit
	memMB float64 // 0 is no limit
}

type ctrState struct {
	name, image, state, health string
	restarts                   int
	exit                       *int
	oom                        bool
	finished                   string
}

const (
	maxContainers     = 200
	maxBufferedSample = 20000
	dockerParallel    = 8
)

// dockerSockets are tried in order when no socket is configured.
var dockerSockets = []string{"/var/run/docker.sock", "/run/podman/podman.sock"}

func newDockerCollector(a *agent, setting string) *dockerCollector {
	return &dockerCollector{a: a, setting: strings.TrimPrefix(strings.TrimSpace(setting), "unix://"),
		prev: map[string]statsReading{}, limits: map[string]ctrLimits{}}
}

// socket finds the engine's socket: the configured one, DOCKER_HOST, then
// the usual Docker and Podman paths. "" when there is none.
func (d *dockerCollector) socket() string {
	if d.sock != "" {
		return d.sock
	}
	candidates := dockerSockets
	if d.setting != "" {
		candidates = []string{d.setting}
	} else if h := os.Getenv("DOCKER_HOST"); strings.HasPrefix(h, "unix://") {
		candidates = append([]string{strings.TrimPrefix(h, "unix://")}, candidates...)
	}
	for _, p := range candidates {
		// A configured path is trusted to be a socket; found ones must be.
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (d.setting != "" || fi.Mode()&os.ModeSocket != 0) {
			d.sock = p
			d.client = &http.Client{Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var dl net.Dialer
					return dl.DialContext(ctx, "unix", p)
				},
				MaxIdleConnsPerHost: dockerParallel,
			}}
			d.a.log.Info("container engine found", "socket", p)
			return p
		}
	}
	if d.setting != "" {
		d.a.once("docker-missing", "container engine socket not found", "socket", d.setting)
	}
	return ""
}

func (d *dockerCollector) get(ctx context.Context, path string, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://engine"+path, nil)
	if err != nil {
		return err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s %s", path, resp.Status, strings.TrimSpace(string(b)))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(out)
}

// listed is one entry of GET /containers/json.
type listed struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Image  string            `json:"Image"`
	State  string            `json:"State"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		IP          string `json:"IP"`
		PrivatePort int    `json:"PrivatePort"`
		PublicPort  int    `json:"PublicPort"`
		Type        string `json:"Type"`
	} `json:"Ports"`
}

func (l listed) name() string {
	if len(l.Names) == 0 {
		return shortID(l.ID)
	}
	return hostName(strings.TrimPrefix(l.Names[0], "/"))
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (d *dockerCollector) list(ctx context.Context, all bool) ([]listed, error) {
	path := "/containers/json"
	if all {
		path += "?all=1"
	}
	var out []listed
	if err := d.get(ctx, path, &out); err != nil {
		// A restarted engine may come back on another socket.
		d.sock = ""
		return nil, err
	}
	if len(out) > maxContainers {
		out = out[:maxContainers]
	}
	return out, nil
}

// each runs f for every container, a few at a time.
func each[T any](xs []T, f func(T)) {
	sem := make(chan struct{}, dockerParallel)
	var wg sync.WaitGroup
	for _, x := range xs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			f(x)
		}()
	}
	wg.Wait()
}

// dockerStats is the part of GET /containers/{id}/stats the agent reads.
type dockerStats struct {
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
		OnlineCPUs int `json:"online_cpus"`
		Throttling struct {
			Periods          uint64 `json:"periods"`
			ThrottledPeriods uint64 `json:"throttled_periods"`
		} `json:"throttling_data"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytes []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// sample takes one stats reading of every running container.
func (d *dockerCollector) sample(ctx context.Context) {
	if d.socket() == "" {
		return
	}
	running, err := d.list(ctx, false)
	if err != nil {
		d.a.once("docker-list", "containers unavailable", "err", err)
		return
	}
	var mu sync.Mutex
	at := time.Now().UTC()
	seen := map[string]bool{}
	each(running, func(c listed) {
		var st dockerStats
		// one-shot skips the engine's own second reading (API 1.41+);
		// older engines ignore it and answer a second later.
		if err := d.get(ctx, "/containers/"+c.ID+"/stats?stream=false&one-shot=true", &st); err != nil {
			d.a.once("docker-stats", "container stats unavailable", "err", err)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		seen[c.ID] = true
		lim, ok := d.limits[c.ID]
		if !ok {
			lim = d.inspectLimits(ctx, c.ID)
			d.limits[c.ID] = lim
		}
		values, reading := statsValues(st, d.prev[c.ID], at, lim)
		d.prev[c.ID] = reading
		if len(d.samples) < maxBufferedSample {
			d.samples = append(d.samples, agentapi.ContainerSample{At: at.Format(time.RFC3339Nano), Container: c.name(), Values: values})
		}
	})
	for id := range d.prev {
		if !seen[id] {
			delete(d.prev, id)
		}
	}
}

// statsValues turns a stats reading into container_metrics values; rates
// need the previous reading of the same container.
func statsValues(st dockerStats, prev statsReading, at time.Time, lim ctrLimits) (map[string]float64, statsReading) {
	cur := statsReading{at: at, cpu: st.CPUStats.CPUUsage.TotalUsage,
		periods: st.CPUStats.Throttling.Periods, throttle: st.CPUStats.Throttling.ThrottledPeriods}
	for _, n := range st.Networks {
		cur.rx += n.RxBytes
		cur.tx += n.TxBytes
	}
	for _, b := range st.BlkioStats.IOServiceBytes {
		switch strings.ToLower(b.Op) {
		case "read":
			cur.read += b.Value
		case "write":
			cur.write += b.Value
		}
	}
	v := map[string]float64{}
	// Memory as docker stats shows it: usage without the inactive page cache.
	used := st.MemoryStats.Usage
	for _, k := range []string{"inactive_file", "total_inactive_file"} {
		if c, ok := st.MemoryStats.Stats[k]; ok && c < used {
			used -= c
			break
		}
	}
	memLimit := lim.memMB
	if memLimit == 0 && st.MemoryStats.Limit > 0 && st.MemoryStats.Limit < 1<<60 {
		memLimit = float64(st.MemoryStats.Limit) / (1 << 20)
	}
	if st.MemoryStats.Usage > 0 {
		v["mem_used_mb"] = float64(used) / (1 << 20)
		if memLimit > 0 {
			v["mem_limit_mb"] = memLimit
			v["mem_percent"] = 100 * v["mem_used_mb"] / memLimit
		}
	}
	if st.PidsStats.Current > 0 {
		v["pids"] = float64(st.PidsStats.Current)
	}
	dt := at.Sub(prev.at).Seconds()
	if prev.at.IsZero() || dt <= 0 || cur.cpu < prev.cpu {
		return v, cur // the first reading, or the container restarted
	}
	cores := float64(cur.cpu-prev.cpu) / 1e9 / dt
	v["cpu_cores"] = cores
	if capacity := lim.cpu; capacity > 0 {
		v["cpu_percent"] = 100 * cores / capacity
	} else if n := st.CPUStats.OnlineCPUs; n > 0 {
		v["cpu_percent"] = 100 * cores / float64(n)
	}
	if p := cur.periods - prev.periods; cur.periods >= prev.periods && p > 0 {
		v["cpu_throttled_percent"] = 100 * float64(cur.throttle-prev.throttle) / float64(p)
	}
	rate := func(now, before uint64) (float64, bool) {
		if now < before {
			return 0, false
		}
		return float64(now-before) / 1024 / dt, true
	}
	if len(st.Networks) > 0 {
		if r, ok := rate(cur.rx, prev.rx); ok {
			v["net_rx_kbps"] = r
		}
		if r, ok := rate(cur.tx, prev.tx); ok {
			v["net_tx_kbps"] = r
		}
	}
	if len(st.BlkioStats.IOServiceBytes) > 0 {
		if r, ok := rate(cur.read, prev.read); ok {
			v["blk_read_kbps"] = r
		}
		if r, ok := rate(cur.write, prev.write); ok {
			v["blk_write_kbps"] = r
		}
	}
	return v, cur
}

func (d *dockerCollector) takeSamples() []agentapi.ContainerSample {
	out := d.samples
	d.samples = nil
	return out
}

// inspected is the part of GET /containers/{id}/json the agent reads.
type inspected struct {
	RestartCount int `json:"RestartCount"`
	State        struct {
		Status     string `json:"Status"`
		OOMKilled  bool   `json:"OOMKilled"`
		ExitCode   int    `json:"ExitCode"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
		Health     *struct {
			Status string `json:"Status"`
		} `json:"Health"`
	} `json:"State"`
	HostConfig struct {
		NanoCPUs    int64    `json:"NanoCpus"`
		CPUQuota    int64    `json:"CpuQuota"`
		CPUPeriod   int64    `json:"CpuPeriod"`
		Memory      int64    `json:"Memory"`
		Privileged  bool     `json:"Privileged"`
		NetworkMode string   `json:"NetworkMode"`
		PidMode     string   `json:"PidMode"`
		CapAdd      []string `json:"CapAdd"`
		Binds       []string `json:"Binds"`
	} `json:"HostConfig"`
	Image  string `json:"Image"` // the image ID, sha256:...
	Config struct {
		User string `json:"User"`
	} `json:"Config"`
	Mounts []struct {
		Source string `json:"Source"`
	} `json:"Mounts"`
}

func (in inspected) limits() ctrLimits {
	var l ctrLimits
	switch hc := in.HostConfig; {
	case hc.NanoCPUs > 0:
		l.cpu = float64(hc.NanoCPUs) / 1e9
	case hc.CPUQuota > 0 && hc.CPUPeriod > 0:
		l.cpu = float64(hc.CPUQuota) / float64(hc.CPUPeriod)
	}
	if in.HostConfig.Memory > 0 {
		l.memMB = float64(in.HostConfig.Memory) / (1 << 20)
	}
	return l
}

func (d *dockerCollector) inspectLimits(ctx context.Context, id string) ctrLimits {
	var in inspected
	if err := d.get(ctx, "/containers/"+id+"/json", &in); err != nil {
		return ctrLimits{}
	}
	return in.limits()
}

// report lists every container with its state, and the events since the
// previous report. Both are nil when no engine answers.
func (d *dockerCollector) report(ctx context.Context) ([]agentapi.Container, []agentapi.ContainerEvent) {
	if d.socket() == "" {
		return nil, nil
	}
	all, err := d.list(ctx, true)
	if err != nil {
		d.a.once("docker-list", "containers unavailable", "err", err)
		return nil, nil
	}
	details := make(map[string]inspected, len(all))
	var mu sync.Mutex
	each(all, func(c listed) {
		var in inspected
		if err := d.get(ctx, "/containers/"+c.ID+"/json", &in); err != nil {
			return
		}
		mu.Lock()
		details[c.ID] = in
		mu.Unlock()
	})

	out := make([]agentapi.Container, 0, len(all))
	states := make(map[string]ctrState, len(all))
	limits := make(map[string]ctrLimits, len(all))
	for _, c := range all {
		in, ok := details[c.ID]
		state := c.State
		if ok && in.State.Status != "" {
			state = in.State.Status
		}
		ctr := agentapi.Container{ID: c.ID, Name: c.name(), Image: c.Image, State: state, Ports: ports(c)}
		ctr.Project = c.Labels["com.docker.compose.project"]
		if ok {
			lim := in.limits()
			limits[c.ID] = lim
			ctr.CPULimit, ctr.MemLimitMB = lim.cpu, lim.memMB
			ctr.Restarts, ctr.OOMKilled = in.RestartCount, in.State.OOMKilled
			ctr.StartedAt, ctr.FinishedAt = dockerTime(in.State.StartedAt), dockerTime(in.State.FinishedAt)
			if ctr.FinishedAt != "" {
				code := in.State.ExitCode
				ctr.ExitCode = &code
			}
			if in.State.Health != nil {
				ctr.Health = in.State.Health.Status
			}
		}
		out = append(out, ctr)
		states[c.ID] = ctrState{name: ctr.Name, image: ctr.Image, state: state, health: ctr.Health,
			restarts: ctr.Restarts, exit: ctr.ExitCode, oom: ctr.OOMKilled, finished: ctr.FinishedAt}
	}
	var events []agentapi.ContainerEvent
	if d.states != nil {
		for id, now := range states {
			if was, ok := d.states[id]; ok {
				events = append(events, containerEvents(was, now)...)
			}
		}
		sort.Slice(events, func(i, j int) bool { return events[i].Container < events[j].Container })
	}
	d.states, d.limits = states, limits
	if d.risks != nil {
		d.risks.check(ctx, d, all, details)
	}
	return out, events
}

// containerEvents compares one container across two reports.
func containerEvents(was, now ctrState) []agentapi.ContainerEvent {
	var out []agentapi.ContainerEvent
	ev := func(kind, detail string, count int) {
		out = append(out, agentapi.ContainerEvent{Kind: kind, Container: now.name, Image: now.image, ExitCode: now.exit, Detail: detail, Count: count})
	}
	exit := ""
	if now.exit != nil {
		exit = "exit code " + strconv.Itoa(*now.exit)
	}
	if n := now.restarts - was.restarts; n > 0 {
		ev("restarted", strings.TrimSpace(exit+" before the last restart"), n)
	}
	if now.oom && now.finished != was.finished {
		ev("oom_killed", "killed for running out of memory", 1)
	}
	if was.state == "running" && (now.state == "exited" || now.state == "dead") {
		ev("exited", exit, 1)
	}
	if now.health == "unhealthy" && was.health != "unhealthy" {
		ev("unhealthy", "health check failing", 1)
	} else if now.health == "healthy" && was.health == "unhealthy" {
		ev("healthy", "health check passing again", 1)
	}
	return out
}

// ports is the published ports, e.g. "0.0.0.0:8080->80/tcp", once each
// (Docker lists the IPv4 and IPv6 bindings separately).
func ports(c listed) string {
	seen := map[string]bool{}
	var out []string
	for _, p := range c.Ports {
		if p.PublicPort == 0 {
			continue
		}
		key := fmt.Sprintf("%d->%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		if seen[key] {
			continue
		}
		seen[key] = true
		ip := p.IP
		if ip == "" || ip == "::" {
			ip = "0.0.0.0"
		}
		out = append(out, ip+":"+key)
	}
	return strings.Join(out, ", ")
}

// dockerTime is an RFC 3339 time, or "" for Docker's zero time.
func dockerTime(v string) string {
	t, err := time.Parse(time.RFC3339Nano, v)
	if err != nil || t.Year() < 2000 {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}
