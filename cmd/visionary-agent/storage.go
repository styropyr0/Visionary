package main

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// The storage scan finds what fills each disk. It is the agent's only heavy
// job, so it runs rarely, on one thread at the lowest CPU and I/O priority,
// paced, and within a time and file budget. Container runtimes' directories
// are not walked; the engine reports their sizes instead.
const (
	storageFirstDelay = 3 * time.Minute
	storageMaxTime    = 10 * time.Minute
	storageMaxEntries = 2_000_000
	storagePaceEvery  = 1000
	storagePause      = 20 * time.Millisecond // at most ~50k files a second
	storageMaxLinks   = 200_000

	storageTopDirs     = 20
	storageExpandDirs  = 6
	storageTopChildren = 8
	storageTopImages   = 15
	storageTopVolumes  = 30
	storageMaxItems    = 400
)

// storageSkip are walked by nothing: kernel filesystems, and the data of
// container runtimes, which the engine measures far more cheaply.
var storageSkip = []string{"/proc", "/sys", "/dev", "/run", "/var/lib/docker", "/var/lib/containers", "/var/lib/containerd", "/var/lib/kubelet"}

type storageScanner struct {
	a     *agent
	every time.Duration
	mu    sync.Mutex
	ready *agentapi.Storage
}

func (s *storageScanner) run(ctx context.Context) {
	wait := storageFirstDelay
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		st := s.scan(ctx)
		s.mu.Lock()
		s.ready = st
		s.mu.Unlock()
		wait = s.every
	}
}

// take hands the latest scan to one report
func (s *storageScanner) take() *agentapi.Storage {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.ready
	s.ready = nil
	return st
}

func (s *storageScanner) scan(ctx context.Context) *agentapi.Storage {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, storageMaxTime)
	defer cancel()
	st := &agentapi.Storage{At: start.UTC().Format(time.RFC3339Nano)}

	done := make(chan struct{})
	go func() {
		// The thread is lowered for the walk and never unlocked, so it ends
		// with this goroutine instead of going back to the agent at low priority.
		runtime.LockOSThread()
		lowPriority()
		defer close(done)
		w := &storageWalk{ctx: ctx, links: map[[2]uint64]struct{}{}}
		for _, m := range s.a.mounts() {
			if ctx.Err() != nil {
				break
			}
			st.Items = append(st.Items, w.mount(m.Mount)...)
		}
		st.Partial = w.partial
	}()
	<-done
	if s.a.cfg.docker != "off" {
		items, err := engineStorage(ctx, s.a.cfg.docker)
		if err != nil {
			s.a.once("storage-engine", "container engine disk usage unavailable", "err", err)
		}
		st.Items = append(st.Items, items...)
	}
	if len(st.Items) > storageMaxItems {
		st.Items = st.Items[:storageMaxItems]
	}
	st.TookS = round(time.Since(start).Seconds(), 1)
	s.a.log.Info("storage scanned", "took", time.Since(start).Round(time.Second).String(), "items", len(st.Items), "partial", st.Partial)
	return st
}

type storageWalk struct {
	ctx     context.Context
	entries int
	links   map[[2]uint64]struct{}
	partial bool
}

type storageSize struct{ bytes, files int64 }

// mount sizes everything on one filesystem by its first two levels: the
// biggest top-level entries, and the biggest entries inside the largest of them.
func (w *storageWalk) mount(mp string) []agentapi.StorageItem {
	root := hostPath(mp)
	fi, err := os.Lstat(root)
	if err != nil || !fi.IsDir() {
		return nil
	}
	_, dev, _, _ := diskUse(fi)
	top := map[string]*storageSize{}
	second := map[string]map[string]*storageSize{}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		p := filepath.Join(root, e.Name())
		if w.skipped(p) {
			continue
		}
		t := &storageSize{}
		top[e.Name()] = t
		if !e.IsDir() {
			w.add(p, e, t)
			continue
		}
		kids := map[string]*storageSize{}
		second[e.Name()] = kids
		if !w.sameDev(p, dev) {
			continue
		}
		inner, err := os.ReadDir(p)
		if err != nil {
			continue
		}
		for _, k := range inner {
			kp := filepath.Join(p, k.Name())
			if w.skipped(kp) {
				continue
			}
			ks := &storageSize{}
			kids[k.Name()] = ks
			if k.IsDir() {
				if w.sameDev(kp, dev) {
					w.tree(kp, dev, ks)
				}
			} else {
				w.add(kp, k, ks)
			}
			t.bytes += ks.bytes
			t.files += ks.files
		}
	}

	names := biggest(top, storageTopDirs)
	var out []agentapi.StorageItem
	for i, n := range names {
		out = append(out, agentapi.StorageItem{Mount: mp, Kind: "dir", Path: joinMount(mp, n), Bytes: top[n].bytes, Files: top[n].files})
		if i >= storageExpandDirs {
			continue
		}
		for _, k := range biggest(second[n], storageTopChildren) {
			c := second[n][k]
			out = append(out, agentapi.StorageItem{Mount: mp, Kind: "dir", Path: joinMount(mp, n+"/"+k), Bytes: c.bytes, Files: c.files})
		}
	}
	return out
}

// tree adds up everything under dir that is on dev, iteratively so a deep
// tree does not grow the stack, and stops at the budget.
func (w *storageWalk) tree(dir string, dev uint64, into *storageSize) {
	stack := []string{dir}
	for len(stack) > 0 {
		if w.stop() {
			return
		}
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			p := filepath.Join(d, e.Name())
			if e.IsDir() {
				if !w.skipped(p) && w.sameDev(p, dev) {
					stack = append(stack, p)
				}
				continue
			}
			w.add(p, e, into)
		}
	}
}

func (w *storageWalk) add(p string, e fs.DirEntry, into *storageSize) {
	fi, err := e.Info()
	if err != nil {
		return
	}
	w.tick()
	b, dev, ino, links := diskUse(fi)
	if links > 1 && ino != 0 {
		k := [2]uint64{dev, ino}
		if _, seen := w.links[k]; seen {
			return
		}
		if len(w.links) < storageMaxLinks {
			w.links[k] = struct{}{}
		}
	}
	into.bytes += b
	into.files++
}

// sameDev is false for another filesystem mounted inside this one
func (w *storageWalk) sameDev(p string, dev uint64) bool {
	fi, err := os.Lstat(p)
	if err != nil || !fi.IsDir() {
		return false
	}
	w.tick()
	_, d, _, _ := diskUse(fi)
	return d == dev || d == 0
}

func (w *storageWalk) tick() {
	w.entries++
	if w.entries%storagePaceEvery == 0 {
		time.Sleep(storagePause)
	}
}

func (w *storageWalk) stop() bool {
	if w.entries >= storageMaxEntries || w.ctx.Err() != nil {
		w.partial = true
		return true
	}
	return false
}

func (w *storageWalk) skipped(p string) bool {
	return underAny(fromHost(p), storageSkip)
}

func biggest(m map[string]*storageSize, n int) []string {
	names := make([]string, 0, len(m))
	for k, v := range m {
		if v.bytes > 0 {
			names = append(names, k)
		}
	}
	sort.Slice(names, func(i, j int) bool { return m[names[i]].bytes > m[names[j]].bytes })
	if len(names) > n {
		names = names[:n]
	}
	return names
}

func joinMount(mp, rel string) string {
	return strings.TrimSuffix(mp, "/") + "/" + rel
}

// dockerDF is the part of GET /system/df the scan uses
type dockerDF struct {
	LayersSize int64 `json:"LayersSize"`
	Images     []struct {
		ID       string   `json:"Id"`
		RepoTags []string `json:"RepoTags"`
		Size     int64    `json:"Size"`
	} `json:"Images"`
	Containers []struct {
		Names  []string `json:"Names"`
		SizeRw int64    `json:"SizeRw"`
	} `json:"Containers"`
	Volumes []struct {
		Name      string   `json:"Name"`
		UsageData *dfUsage `json:"UsageData"`
	} `json:"Volumes"`
	BuildCache []struct {
		Size   int64 `json:"Size"`
		Shared bool  `json:"Shared"`
	} `json:"BuildCache"`
}

// engineStorage asks Docker or Podman what its images, containers, volumes
// and build cache take. It opens its own connection, since the scan runs
// beside the collector's loop.
func engineStorage(ctx context.Context, setting string) ([]agentapi.StorageItem, error) {
	sock := ""
	for _, p := range socketCandidates(strings.TrimPrefix(strings.TrimSpace(setting), "unix://")) {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && (setting != "" || fi.Mode()&os.ModeSocket != 0) {
			sock = p
			break
		}
	}
	if sock == "" {
		return nil, nil
	}
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "unix", sock)
	}}
	defer tr.CloseIdleConnections()
	d := &dockerCollector{client: &http.Client{Transport: tr}}
	var df dockerDF
	// The engine sizes every container and volume, which can take a while on a busy host
	if err := d.getWithin(ctx, 3*time.Minute, "/system/df", &df); err != nil {
		return nil, err
	}
	return dfItems(df), nil
}

func dfItems(df dockerDF) []agentapi.StorageItem {
	var out []agentapi.StorageItem
	if df.LayersSize > 0 {
		out = append(out, agentapi.StorageItem{Kind: "docker_images", Path: "images", Bytes: df.LayersSize})
	}
	imgs := df.Images
	sort.Slice(imgs, func(i, j int) bool { return imgs[i].Size > imgs[j].Size })
	for i, im := range imgs {
		if i >= storageTopImages || im.Size <= 0 {
			break
		}
		name := shortID(strings.TrimPrefix(im.ID, "sha256:"))
		for _, t := range im.RepoTags {
			if t != "<none>:<none>" {
				name = t
				break
			}
		}
		out = append(out, agentapi.StorageItem{Kind: "docker_image", Path: name, Bytes: im.Size})
	}
	for _, c := range df.Containers {
		if c.SizeRw <= 0 || len(c.Names) == 0 {
			continue
		}
		out = append(out, agentapi.StorageItem{Kind: "docker_container", Path: hostName(strings.TrimPrefix(c.Names[0], "/")), Bytes: c.SizeRw})
	}
	vols := df.Volumes
	sort.Slice(vols, func(i, j int) bool { return volSize(vols[i].UsageData) > volSize(vols[j].UsageData) })
	for i, v := range vols {
		if i >= storageTopVolumes || volSize(v.UsageData) <= 0 {
			break
		}
		out = append(out, agentapi.StorageItem{Kind: "docker_volume", Path: v.Name, Bytes: v.UsageData.Size})
	}
	var cache int64
	for _, b := range df.BuildCache {
		if !b.Shared {
			cache += b.Size
		}
	}
	if cache > 0 {
		out = append(out, agentapi.StorageItem{Kind: "docker_build_cache", Path: "build cache", Bytes: cache})
	}
	return out
}

type dfUsage struct {
	Size int64 `json:"Size"`
}

// volSize is -1 when the engine did not size the volume
func volSize(u *dfUsage) int64 {
	if u == nil {
		return -1
	}
	return u.Size
}

// parseScanEvery reads VISIONARY_AGENT_STORAGE_SCAN: a duration, or "off"
func parseScanEvery(v string) (time.Duration, error) {
	switch v = strings.ToLower(strings.TrimSpace(v)); v {
	case "":
		return 6 * time.Hour, nil
	case "off", "0", "false", "no":
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 10*time.Minute {
		return 0, errors.New("VISIONARY_AGENT_STORAGE_SCAN must be off or a duration of at least 10m, such as 6h")
	}
	return d, nil
}

func scanEveryText(d time.Duration) string {
	if d == 0 {
		return "off"
	}
	return d.String()
}
