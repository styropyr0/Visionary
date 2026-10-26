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
// It runs on the host itself (deploy/agent/install.sh), not in a container.
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

	// Security collectors that run when security is on.
	sshKeys        bool
	fileIntegrity  bool
	processChecks  bool
	containerRisks bool
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
	fs.StringVar(&c.docker, "docker", os.Getenv("VISIONARY_AGENT_DOCKER"), `container engine socket: a path, "off", or empty to find Docker or Podman (VISIONARY_AGENT_DOCKER)`)
	showVersion := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if *showVersion {
		fmt.Println("visionary-agent", version)
		return c, flag.ErrHelp
	}
	c.units, c.disks = splitList(*units), splitList(*disks)
	c.url = strings.TrimRight(strings.TrimSpace(c.url), "/")
	u, err := url.Parse(c.url)
	if c.url == "" || err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return c, errors.New("VISIONARY_URL must be the server's http(s) URL")
	}
	if c.key = strings.TrimSpace(c.key); c.key == "" {
		return c, errors.New("VISIONARY_KEY must be a server ingest key")
	}
	if c.interval < time.Second {
		return c, errors.New("the sample interval must be at least 1s")
	}
	if c.report < c.interval {
		c.report = c.interval
	}
	if c.host == "" {
		c.host, _ = os.Hostname()
	}
	c.host = hostName(c.host)
	return c, nil
}

func run(ctx context.Context, cfg config, logger *slog.Logger) error {
	if strings.HasPrefix(cfg.url, "http://") {
		logger.Warn("VISIONARY_URL is plain http; the ingest key travels unencrypted")
	}
	diskPath := "/"
	if len(cfg.disks) > 0 {
		diskPath = cfg.disks[0]
	}
	sampler := visionary.NewHostSampler(diskPath, cfg.interval)
	a := newAgent(cfg, logger)
	snd := newSender(cfg, logger)
	go snd.run(ctx)
	if cfg.security {
		go a.updates.run(ctx)
	}
	logger.Info("visionary-agent started", "version", version, "host", cfg.host, "server", cfg.url,
		"interval", cfg.interval.String(), "report", cfg.report.String(), "security", cfg.security, "docker", fallbackStr(cfg.docker, "auto"))

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
	return agentapi.Sample{At: at, Values: values}
}

// procPath is a path under /proc.
func procPath(parts ...string) string {
	return filepath.Join(append([]string{"/proc"}, parts...)...)
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

func envBool(key string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}
