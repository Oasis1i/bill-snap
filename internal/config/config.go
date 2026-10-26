package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

// Config is process configuration. The HTTP server requires UploadToken.
// The MCP process only needs the database path and time zone.
type Config struct {
	HTTPAddr       string
	UploadToken    string
	DBPath         string
	UploadDir      string
	Timezone       string
	Location       *time.Location
	VisionProvider string
	VisionAPIKey   string
	VisionBaseURL  string
	VisionModel    string
	MaxUploadBytes int64
}

// Load reads an optional .env file in the working directory, then the
// environment. Variables already set in the environment win over .env.
func Load() (Config, error) {
	if err := loadEnvFile(".env"); err != nil {
		return Config{}, err
	}
	tz := getenv("APP_TZ", "Asia/Shanghai")
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return Config{}, fmt.Errorf("APP_TZ: %w", err)
	}
	maxBytes := int64(8 << 20)
	if raw := os.Getenv("MAX_UPLOAD_BYTES"); strings.TrimSpace(raw) != "" {
		n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil || n <= 0 {
			return Config{}, fmt.Errorf("MAX_UPLOAD_BYTES must be a positive integer")
		}
		maxBytes = n
	}
	return Config{
		HTTPAddr:       httpAddr(),
		UploadToken:    strings.TrimSpace(os.Getenv("UPLOAD_TOKEN")),
		DBPath:         getenv("DB_PATH", "data/bills.db"),
		UploadDir:      getenv("UPLOAD_DIR", "data/uploads"),
		Timezone:       tz,
		Location:       loc,
		VisionProvider: getenv("VISION_PROVIDER", "mock"),
		VisionAPIKey:   strings.TrimSpace(os.Getenv("VISION_API_KEY")),
		VisionBaseURL:  strings.TrimSpace(os.Getenv("VISION_BASE_URL")),
		VisionModel:    strings.TrimSpace(os.Getenv("VISION_MODEL")),
		MaxUploadBytes: maxBytes,
	}, nil
}

// ValidateForServer checks settings the HTTP API cannot run without.
func (c Config) ValidateForServer() error {
	if strings.TrimSpace(c.UploadToken) == "" {
		return fmt.Errorf("UPLOAD_TOKEN is required")
	}
	return nil
}

func httpAddr() string {
	if addr := strings.TrimSpace(os.Getenv("HTTP_ADDR")); addr != "" {
		return addr
	}
	if port := strings.TrimSpace(os.Getenv("PORT")); port != "" {
		return ":" + port
	}
	return "127.0.0.1:8080"
}

func getenv(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

// loadEnvFile sets keys that are not already present in the environment.
// A missing file is not an error.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		line = strings.TrimSpace(line)
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		if key == "" {
			continue
		}
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return err
		}
	}
	return sc.Err()
}
