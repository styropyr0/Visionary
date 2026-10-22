// Package analytics is the Visionary server: fixed dashboards over the D1
// request log and system metrics, an AI analyst that investigates questions
// with read-only SQL, saved panels, and a request log explorer.
package analytics

import (
	"errors"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/styropyr0/visionary/internal/authbridge"
	"github.com/styropyr0/visionary/sdks/go/d1"
)

type Config struct {
	Addr string

	DatabaseURL  string
	D1APIBase    string
	D1AccountID  string
	D1DatabaseID string
	D1APIToken   string

	OpenAIAPIKey   string
	OpenAIAPIBase  string
	Model          string
	ModelReasoning string
	MaxRows        int
	MaxRangeDays   int
	// AppName is the product whose API is being analysed ("Acme"); the
	// analyst uses it in answers. DataNotes is free text about the data the
	// analyst should know, such as what user_role values mean.
	AppName   string
	DataNotes string

	// Auth is delegated to an existing API; see internal/authbridge.
	Auth            authbridge.Config
	SessionSecret   string
	SessionTTL      time.Duration
	RoleCheckEvery  time.Duration
	TrustProxy      bool
	SecureCookieEnv string
}

// LoadConfig reads the environment. D1 variables use the same names as the
// API server that writes the tables.
func LoadConfig() (Config, error) {
	cfg := Config{
		Addr:            getEnv("VISIONARY_ADDR", ":8090"),
		DatabaseURL:     os.Getenv("VISIONARY_DATABASE_URL"),
		D1APIBase:       getEnv("D1_API_BASE", d1.DefaultAPIBase),
		D1AccountID:     os.Getenv("D1_ACCOUNT_ID"),
		D1DatabaseID:    os.Getenv("D1_DATABASE_ID"),
		D1APIToken:      os.Getenv("D1_API_TOKEN"),
		OpenAIAPIKey:    os.Getenv("OPENAI_API_KEY"),
		OpenAIAPIBase:   getEnv("OPENAI_API_BASE", "https://api.openai.com/v1"),
		Model:           getEnv("VISIONARY_MODEL", "gpt-4.1"),
		ModelReasoning:  strings.ToLower(os.Getenv("VISIONARY_REASONING")),
		MaxRows:         getInt("VISIONARY_MAX_ROWS", 5000),
		MaxRangeDays:    getInt("VISIONARY_MAX_RANGE_DAYS", 90),
		AppName:         getEnv("VISIONARY_APP_NAME", "the application"),
		DataNotes:       strings.TrimSpace(os.Getenv("VISIONARY_DATA_NOTES")),
		SessionSecret:   os.Getenv("VISIONARY_SESSION_SECRET"),
		SessionTTL:      getDuration("VISIONARY_SESSION_TTL", 12*time.Hour),
		RoleCheckEvery:  getDuration("VISIONARY_ROLE_CHECK_INTERVAL", 5*time.Minute),
		TrustProxy:      getBool("VISIONARY_TRUST_PROXY", false),
		SecureCookieEnv: strings.ToLower(getEnv("VISIONARY_SECURE_COOKIE", "auto")),
	}

	var missing []string
	for name, v := range map[string]string{
		"D1_ACCOUNT_ID":  cfg.D1AccountID,
		"D1_DATABASE_ID": cfg.D1DatabaseID,
		"D1_API_TOKEN":   cfg.D1APIToken,
	} {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, name)
		}
	}
	usesD1 := cfg.DatabaseURL == "" || strings.HasPrefix(strings.ToLower(cfg.DatabaseURL), "d1:")
	if usesD1 && len(missing) > 0 {
		sort.Strings(missing)
		return cfg, errors.New("set VISIONARY_DATABASE_URL (postgres://, mysql://, sqlite:///path), or for Cloudflare D1: " + strings.Join(missing, ", "))
	}

	auth, err := authbridge.Load(authbridge.Source{
		Path:   strings.TrimSpace(os.Getenv("VISIONARY_AUTH_CONFIG")),
		Inline: strings.TrimSpace(os.Getenv("VISIONARY_AUTH_CONFIG_JSON")),
		Getenv: os.Getenv,
	})
	if err != nil {
		return cfg, err
	}
	cfg.Auth = auth
	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func getInt(key string, fallback int) int {
	if v, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return fallback
}

func getBool(key string, fallback bool) bool {
	if v, err := strconv.ParseBool(strings.TrimSpace(os.Getenv(key))); err == nil {
		return v
	}
	return fallback
}

func getDuration(key string, fallback time.Duration) time.Duration {
	if v, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key))); err == nil && v > 0 {
		return v
	}
	return fallback
}

// getFloat reads a percentage in (0, 100).
func getFloat(key string, fallback float64) float64 {
	if v, err := strconv.ParseFloat(strings.TrimSpace(os.Getenv(key)), 64); err == nil && v > 0 && v < 100 {
		return v
	}
	return fallback
}
