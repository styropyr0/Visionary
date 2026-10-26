package main

import (
	"context"
	"sort"
	"strings"

	"github.com/styropyr0/visionary/internal/agentapi"
)

// --- container risks ---

// riskTracker turns the containers' inspect data into security events:
// running containers that are privileged or share the host's namespaces,
// that run as root, or that run an image this host never ran before. Each
// container ID reports each finding once. New images need a baseline: the
// images on the host and in use at the first report; the agent keeps no
// state on disk, so a restart takes a new baseline.
type riskTracker struct {
	reported map[string]map[string]bool // by container ID, the findings sent
	images   map[string]bool            // image IDs seen; nil until the baseline
	events   []agentapi.Event
}

func newRiskTracker() *riskTracker {
	return &riskTracker{reported: map[string]map[string]bool{}}
}

var dockerSocketPaths = map[string]bool{"/var/run/docker.sock": true, "/run/docker.sock": true}

// containerRisks are why a container's setup gives it the host: privileged,
// host network, host PID, docker socket mounted, CAP_SYS_ADMIN, all
// capabilities.
func containerRisks(in inspected) []string {
	var out []string
	hc := in.HostConfig
	if hc.Privileged {
		out = append(out, "privileged")
	}
	if hc.NetworkMode == "host" {
		out = append(out, "host network")
	}
	if hc.PidMode == "host" {
		out = append(out, "host PID")
	}
	sock := false
	for _, m := range in.Mounts {
		sock = sock || dockerSocketPaths[m.Source]
	}
	for _, b := range hc.Binds {
		src, _, _ := strings.Cut(b, ":")
		sock = sock || dockerSocketPaths[src]
	}
	if sock {
		out = append(out, "docker socket mounted")
	}
	var sysAdmin, all bool
	for _, c := range hc.CapAdd {
		switch strings.TrimPrefix(strings.ToUpper(c), "CAP_") {
		case "SYS_ADMIN":
			sysAdmin = true
		case "ALL":
			all = true
		}
	}
	if sysAdmin {
		out = append(out, "CAP_SYS_ADMIN")
	}
	if all {
		out = append(out, "all capabilities")
	}
	return out
}

// runsAsRoot: no user set, or user 0 / root (with any group).
func runsAsRoot(user string) bool {
	u, _, _ := strings.Cut(strings.TrimSpace(user), ":")
	return u == "" || u == "0" || u == "root"
}

func imageID(id string) string { return strings.TrimPrefix(id, "sha256:") }

// check looks at every running container of this report.
func (r *riskTracker) check(ctx context.Context, d *dockerCollector, all []listed, details map[string]inspected) {
	baseline := r.images == nil
	if baseline {
		r.images = map[string]bool{}
		var imgs []struct {
			ID string `json:"Id"`
		}
		if err := d.get(ctx, "/images/json", &imgs); err == nil {
			for _, img := range imgs {
				r.images[imageID(img.ID)] = true
			}
		}
	}
	live := map[string]bool{}
	var used []string
	for _, c := range all {
		live[c.ID] = true
		in, ok := details[c.ID]
		state := c.State
		if ok && in.State.Status != "" {
			state = in.State.Status
		}
		if !ok || state != "running" {
			continue
		}
		name := c.name()
		if risks := containerRisks(in); len(risks) > 0 {
			r.add(c.ID, "privileged "+strings.Join(risks, ","), agentapi.Event{Kind: "container_privileged", Target: name,
				Detail: c.Image + ": " + strings.Join(risks, ", ")})
		}
		if runsAsRoot(in.Config.User) {
			r.add(c.ID, "root", agentapi.Event{Kind: "container_root", Target: name, Detail: c.Image})
		}
		id := imageID(in.Image)
		if id == "" {
			continue
		}
		if !baseline && !r.images[id] {
			r.add(c.ID, "image "+id, agentapi.Event{Kind: "container_new_image", Target: name,
				Detail: strings.TrimSpace(c.Image + " " + shortID(id))})
		}
		used = append(used, id)
	}
	// After the loop, so containers started together from one new image all
	// report it.
	for _, id := range used {
		r.images[id] = true
	}
	for id := range r.reported {
		if !live[id] {
			delete(r.reported, id)
		}
	}
	sort.Slice(r.events, func(i, j int) bool { return r.events[i].Target < r.events[j].Target })
}

func (r *riskTracker) add(id, key string, e agentapi.Event) {
	if r.reported[id] == nil {
		r.reported[id] = map[string]bool{}
	}
	if r.reported[id][key] {
		return
	}
	r.reported[id][key] = true
	e.Count = 1
	r.events = append(r.events, e)
}

// take hands the findings to one report.
func (r *riskTracker) take() []agentapi.Event {
	out := r.events
	r.events = nil
	return out
}
