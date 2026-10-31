// Command visionary-agent watches one machine (a database box, a worker, a
// bastion) and reports it to a Visionary server over HTTPS, so hosts without
// an instrumented app show up too. It never touches the database: the server
// stores what it sends.
//
// Every 15 s it samples the host's CPU, memory, disk and network with the Go
// SDK's sampler; once a minute it posts those samples with the inventory, the
// top processes, every real mount, failed and watched systemd units, and the
// security findings since the last report (SSH logins, sudo, listening ports,
// pending updates, authorized SSH keys, changed system files, suspicious
// processes). With Docker or Podman on the host it also reports every
// container: its state, CPU, memory, network and disk use, and restarts, OOM
// kills, exits and failed health checks. The wire format is internal/agentapi.
//
//	VISIONARY_URL=https://visionary.example.com VISIONARY_KEY=... visionary-agent
//
// It runs on the host itself (deploy/agent/install.sh), or in a container that
// sees the host through HOST_ROOT, HOST_PROC, HOST_SYS and HOST_ETC (see
// Dockerfile.agent)
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/styropyr0/visionary/internal/agentapi"
	visionary "github.com/styropyr0/visionary/sdks/go"
)

// version is set at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

type config struct {
	url      string
	key      string
	host     string
	service  string
	interval time.Duration
	report   time.Duration
	units    []string
	security bool
	disks    []string
	authLog  string // a log file, "journald", "off", or "" to pick one
	docker   string // the engine's socket, "off", or "" to find one
	// journald, docker, or file globs
	logs     []string
	stateDir string

	location       string
	allowPrivate   string
	browserWorkers int

	// Security collectors that run when security is on.
	sshKeys        bool
	fileIntegrity  bool
	processChecks  bool
	containerRisks bool

	gpu bool
}

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, nil))
	cfg, err := loadConfig(os.Args[1:])
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		logger.Error("invalid configuration", "err", err)
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, cfg, logger); err != nil {
		logger.Error("visionary-agent", "err", err)
		os.Exit(1)
	}
}

// loadConfig reads the environment, then flags, which win.
func loadConfig(args []string) (config, error) {
	fs := flag.NewFlagSet("visionary-agent", flag.ContinueOnError)
	c := config{}
	fs.StringVar(&c.url, "url", os.Getenv("VISIONARY_URL"), "Visionary server URL (VISIONARY_URL)")
	fs.StringVar(&c.key, "key", os.Getenv("VISIONARY_KEY"), "server ingest key (VISIONARY_KEY)")
	fs.StringVar(&c.host, "host", os.Getenv("VISIONARY_HOST"), "host name to report (VISIONARY_HOST, default the machine's)")
	fs.StringVar(&c.service, "service", os.Getenv("VISIONARY_SERVICE"), "service the host belongs to (VISIONARY_SERVICE)")
	fs.DurationVar(&c.interval, "interval", envDuration("VISIONARY_AGENT_INTERVAL", 15*time.Second), "sample interval (VISIONARY_AGENT_INTERVAL)")
	fs.DurationVar(&c.report, "report", envDuration("VISIONARY_AGENT_REPORT", time.Minute), "report interval (VISIONARY_AGENT_REPORT)")
	units := fs.String("units", os.Getenv("VISIONARY_AGENT_UNITS"), "comma-separated systemd units to watch (VISIONARY_AGENT_UNITS)")
	fs.BoolVar(&c.security, "security", envBool("VISIONARY_AGENT_SECURITY", true), "collect SSH, sudo, port and update findings (VISIONARY_AGENT_SECURITY)")
	disks := fs.String("disk-paths", os.Getenv("VISIONARY_DISK_PATHS"), "comma-separated mount points to report; the first fills the disk columns (VISIONARY_DISK_PATHS)")
	fs.StringVar(&c.authLog, "auth-log", os.Getenv("VISIONARY_AGENT_AUTH_LOG"), `auth log to read: a file, "journald" or "off" (VISIONARY_AGENT_AUTH_LOG, default auto)`)
	fs.BoolVar(&c.sshKeys, "ssh-keys", envBool("VISIONARY_AGENT_SSH_KEYS", true), "report SSH keys added to or removed from authorized_keys (VISIONARY_AGENT_SSH_KEYS)")
	fs.BoolVar(&c.fileIntegrity, "file-integrity", envBool("VISIONARY_AGENT_FILE_INTEGRITY", true), "report changes to accounts, sudoers, cron, PAM, sshd and systemd files (VISIONARY_AGENT_FILE_INTEGRITY)")
	fs.BoolVar(&c.processChecks, "process-checks", envBool("VISIONARY_AGENT_PROCESS_CHECKS", true), "report miners, reverse shells and programs run from /tmp (VISIONARY_AGENT_PROCESS_CHECKS)")
	fs.BoolVar(&c.containerRisks, "container-risks", envBool("VISIONARY_AGENT_CONTAINER_RISKS", true), "report privileged, root and new-image containers (VISIONARY_AGENT_CONTAINER_RISKS)")
	fs.BoolVar(&c.gpu, "gpu", envBool("VISIONARY_AGENT_GPU", true), "report GPUs through nvidia-smi and amdgpu (VISIONARY_AGENT_GPU)")
	fs.StringVar(&c.docker, "docker", os.Getenv("VISIONARY_AGENT_DOCKER"), `container engine socket: a path, "off", or empty to find Docker or Podman (VISIONARY_AGENT_DOCKER)`)
	logs := fs.String("logs", os.Getenv("VISIONARY_AGENT_LOGS"), `comma-separated log sources to send: journald, docker, and file globs such as /var/log/nginx/*.log (VISIONARY_AGENT_LOGS, default none)`)
	fs.StringVar(&c.stateDir, "state", envStr("VISIONARY_AGENT_STATE", "/var/lib/visionary-agent"), "directory for where log sources got to (VISIONARY_AGENT_STATE)")
	fs.StringVar(&c.location, "location", os.Getenv("VISIONARY_AGENT_LOCATION"), "run the uptime checks of this location instead of reporting the host; VISIONARY_KEY is then the location's key (VISIONARY_AGENT_LOCATION)")
	fs.StringVar(&c.allowPrivate, "allow-private", os.Getenv("VISIONARY_AGENT_ALLOW_PRIVATE"), "true or false: may this location's checks reach private and loopback addresses; unset follows the server's setting (VISIONARY_AGENT_ALLOW_PRIVATE)")
	fs.IntVar(&c.browserWorkers, "browser-workers", envInt("VISIONARY_AGENT_BROWSER_WORKERS", 1), "browser checks run at once in location mode (VISIONARY_AGENT_BROWSER_WORKERS)")
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if *showVersion {
		fmt.Println("visionary-agent", version)
		return c, flag.ErrHelp
	}
	c.units, c.disks, c.logs = splitList(*units), splitList(*disks), splitList(*logs)
	c.url = strings.TrimRight(strings.TrimSpace(c.url), "/")
	u, err := url.Parse(c.url)
	if c.url == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return c, errors.New("VISIONARY_URL must be the server's http(s) URL")
	}
	if c.key = strings.TrimSpace(c.key); c.key == "" {
		return c, errors.New("VISIONARY_KEY must be a server ingest key, or a location key with VISIONARY_AGENT_LOCATION")
	}
	c.location = strings.ToLower(strings.TrimSpace(c.location))
	switch c.allowPrivate = strings.ToLower(strings.TrimSpace(c.allowPrivate)); c.allowPrivate {
	case "", "true", "false":
	case "1", "yes", "on":
		c.allowPrivate = "true"
	case "0", "no", "off":
		c.allowPrivate = "false"
	default:
		return c, errors.New("VISIONARY_AGENT_ALLOW_PRIVATE must be true or false")
	}
	if c.browserWorkers < 1 || c.browserWorkers > 8 {
		return c, errors.New("browser workers must be 1 to 8")
	}
	if c.interval < time.Second {
		return c, errors.New("the sample interval must be at least 1s")
	}
	if c.report < c.interval {
		c.report = c.interval
	}
	if c.host == "" {
		c.host = machineName()
	}
	c.host = hostName(c.host)
	return c, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	if strings.HasPrefix(cfg.url, "http://") {
		logger.Warn("VISIONARY_URL is plain http; the key travels unencrypted")
	}
	if cfg.location != "" {
		return runLocation(ctx, cfg, logger)
	}
	diskPath := "/"
	if len(cfg.disks) > 0 {
		diskPath = cfg.disks[0]
	}
	var opts []visionary.HostOption
	if !cfg.gpu {
		opts = append(opts, visionary.WithoutGPU())
	}
	sampler := visionary.NewHostSampler(hostPath(diskPath), cfg.interval, opts...)
	a := newAgent(cfg, logger)
	a.sampler = sampler
	snd := newSender(cfg, logger)
	go snd.run(ctx)
	if len(cfg.logs) > 0 {
		go newLogShipper(cfg, logger, a.docker).run(ctx)
	}
	if cfg.security {
		go a.updates.run(ctx)
	}
	logger.Info("visionary-agent started", "version", version, "host", cfg.host, "server", cfg.url,
		"interval", cfg.interval.String(), "report", cfg.report.String(), "security", cfg.security, "docker", fallbackStr(cfg.docker, "auto"), "gpu", cfg.gpu,
		"logs", fallbackStr(strings.Join(cfg.logs, ","), "off"))

	sampleT := time.NewTicker(cfg.interval)
	defer sampleT.Stop()
	reportT := time.NewTicker(cfg.report)
	defer reportT.Stop()
	var samples []agentapi.Sample
	for {
		select {
		case <-ctx.Done():
			// One last report, then a few seconds to deliver what is queued.
			if len(samples) > 0 {
				snd.enqueue(a.report(context.Background(), samples))
			}
			fctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			snd.drain(fctx)
			cancel()
			return nil
		case <-sampleT.C:
			samples = append(samples, sample(sampler))
			if a.docker != nil {
				a.docker.sample(ctx)
			}
		case <-reportT.C:
			snd.enqueue(a.report(ctx, samples))
			samples = nil
		}
	}
}

func sample(h *visionary.HostSampler) agentapi.Sample {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	values := map[string]float64{}
	for k, v := range h.Sample() {
		switch t := v.(type) {
		case float64:
			values[k] = t
		case int64:
			values[k] = float64(t)
		case int:
			values[k] = float64(t)
		}
	}
	return agentapi.Sample{At: at, Values: values, GPUs: gpus(h.GPUs())}
}

// hostRoot is where the host's filesystem is: "/" on the host itself, e.g.
// "/host" in a container started with -v /:/host:ro
var hostRoot = rootFrom(os.Getenv("HOST_ROOT"))

func rootFrom(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return "/"
	}
	if c := filepath.Clean(v); c != string(filepath.Separator) {
		return c
	}
	return "/"
}

// hostPath is p, a path on the host, as this process sees it
func hostPath(p string) string {
	if hostRoot == "/" {
		return p
	}
	return filepath.Join(hostRoot, p)
}

// fromHost turns a path under hostRoot back into the host's own path
func fromHost(p string) string {
	if hostRoot == "/" {
		return p
	}
	rel, err := filepath.Rel(hostRoot, p)
	if rel == "." {
		return "/"
	}
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return p
	}
	return "/" + filepath.ToSlash(rel)
}

// procPath is a path under the host's /proc
func procPath(parts ...string) string {
	return filepath.Join(append([]string{envStr("HOST_PROC", "/proc")}, parts...)...)
}

// machineName is the host's /etc/hostname in a container, else the kernel's name
func machineName() string {
	if hostRoot != "/" {
		if b, err := os.ReadFile(hostPath("/etc/hostname")); err == nil {
			if n := strings.TrimSpace(string(b)); n != "" {
				return n
			}
		}
	}
	n, _ := os.Hostname()
	return n
}

func fallbackStr(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

var badHostChars = regexp.MustCompile(`[^A-Za-z0-9._:-]+`)

// hostName fits a name to what the server accepts as an instance.
func hostName(n string) string {
	n = strings.Trim(badHostChars.ReplaceAllString(strings.TrimSpace(n), "-"), "-")
	if len(n) > 128 {
		n = n[:128]
	}
	if n == "" {
		n = "host"
	}
	return n
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envDuration(key string, def time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return time.Duration(n) * time.Second // plain seconds
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil {
		return n
	}
	return def
}

func envStr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}
