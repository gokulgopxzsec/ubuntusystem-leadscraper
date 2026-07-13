package config

import (
	"context"
	"fmt"
	"time"

	"github.com/sethvargo/go-envconfig"
)

type Config struct {
	Environment string `env:"ENVIRONMENT, default=development"`
	Port        int    `env:"PORT, default=8080"`
	LogLevel    string `env:"LOG_LEVEL, default=info"`
	Version     string

	Database DatabaseConfig
	Redis    RedisConfig
	Auth     AuthConfig
	AI       AIConfig
	Crawler  CrawlerConfig
	Worker   WorkerConfig
	Sources  SourcesConfig
}

type DatabaseConfig struct {
	URL               string        `env:"DATABASE_URL, default=postgres://postgres:postgres@localhost:5432/leadscraper?sslmode=disable"`
	MaxConns          int           `env:"DATABASE_MAX_CONNS, default=10"`
	MinConns          int           `env:"DATABASE_MIN_CONNS, default=2"`
	MaxConnLifetime   time.Duration `env:"DATABASE_MAX_CONN_LIFETIME, default=30m"`
	MaxConnIdleTime   time.Duration `env:"DATABASE_MAX_CONN_IDLE_TIME, default=5m"`
	HealthCheckPeriod time.Duration `env:"DATABASE_HEALTH_CHECK_PERIOD, default=1m"`
	MigrationsDir     string        `env:"MIGRATIONS_DIR, default=migrations"`
	AutoMigrate       bool          `env:"AUTO_MIGRATE, default=true"`
}

type RedisConfig struct {
	URL      string `env:"REDIS_URL, default=redis://localhost:6379/0"`
	Password string `env:"REDIS_PASSWORD"`
	QueueKey string `env:"REDIS_QUEUE_KEY, default=leadscraper:jobs"`
}

type AuthConfig struct {
	JWTSecret     string        `env:"JWT_SECRET, default=dev-secret-change-in-production"`
	JWTExpiration time.Duration `env:"JWT_EXPIRATION, default=24h"`
	APIKey        string        `env:"API_KEY"`
}

type AIConfig struct {
	Provider string        `env:"AI_PROVIDER, default=none"`
	APIKey   string        `env:"AI_API_KEY"`
	Model    string        `env:"AI_MODEL, default=gemini-2.0-flash"`
	BaseURL  string        `env:"AI_BASE_URL"`
	Timeout  time.Duration `env:"AI_TIMEOUT, default=60s"`
	// MaxHTMLChars bounds how much page HTML reaches the model. A whole page
	// blows past the context window and costs far more than it adds.
	MaxHTMLChars int `env:"AI_MAX_HTML_CHARS, default=12000"`
}

type CrawlerConfig struct {
	UserAgent     string        `env:"CRAWLER_USER_AGENT, default=leadscraper/0.1 (+https://makeforme.in)"`
	Timeout       time.Duration `env:"CRAWLER_TIMEOUT, default=15s"`
	MaxPages      int           `env:"CRAWLER_MAX_PAGES, default=5"`
	MaxBodyBytes  int64         `env:"CRAWLER_MAX_BODY_BYTES, default=2097152"`
	DelayBetween  time.Duration `env:"CRAWLER_DELAY, default=1s"`
	RespectRobots bool          `env:"CRAWLER_RESPECT_ROBOTS, default=true"`
	StoreHTML     bool          `env:"CRAWLER_STORE_HTML, default=false"`
}

type WorkerConfig struct {
	Concurrency  int           `env:"WORKER_CONCURRENCY, default=4"`
	JobTimeout   time.Duration `env:"WORKER_JOB_TIMEOUT, default=2m"`
	MinJobDelay  time.Duration `env:"WORKER_MIN_JOB_DELAY, default=500ms"`
	MaxJobDelay  time.Duration `env:"WORKER_MAX_JOB_DELAY, default=2s"`
	ShutdownWait time.Duration `env:"WORKER_SHUTDOWN_WAIT, default=30s"`
}

type SourcesConfig struct {
	GooglePlacesAPIKey string `env:"GOOGLE_PLACES_API_KEY"`
	CSVDir             string `env:"CSV_IMPORT_DIR, default=data"`
}

func Load(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfg.Version = version()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func (c *Config) validate() error {
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("invalid PORT: %d", c.Port)
	}
	if c.Database.MinConns > c.Database.MaxConns {
		return fmt.Errorf("DATABASE_MIN_CONNS (%d) exceeds DATABASE_MAX_CONNS (%d)",
			c.Database.MinConns, c.Database.MaxConns)
	}
	if c.Worker.Concurrency < 1 {
		return fmt.Errorf("WORKER_CONCURRENCY must be >= 1, got %d", c.Worker.Concurrency)
	}
	if c.Worker.MinJobDelay > c.Worker.MaxJobDelay {
		return fmt.Errorf("WORKER_MIN_JOB_DELAY exceeds WORKER_MAX_JOB_DELAY")
	}
	switch c.AI.Provider {
	case "", "none", "gemini", "openai":
	default:
		return fmt.Errorf("unsupported AI_PROVIDER %q (want gemini, openai, or none)", c.AI.Provider)
	}
	if (c.AI.Provider == "gemini" || c.AI.Provider == "openai") && c.AI.APIKey == "" {
		return fmt.Errorf("AI_PROVIDER=%s requires AI_API_KEY", c.AI.Provider)
	}
	if c.Environment == "production" && c.Auth.JWTSecret == "dev-secret-change-in-production" {
		return fmt.Errorf("JWT_SECRET must be set in production")
	}
	return nil
}

func version() string {
	return "0.1.0"
}
