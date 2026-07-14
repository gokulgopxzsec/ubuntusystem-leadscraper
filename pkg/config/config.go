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
	Gmaps    GmapsConfig
	Embed    EmbedConfig
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

// EmbedConfig powers semantic search over the leads.
//
// Without a provider the search box falls back to keyword matching rather than
// breaking, and the API says which one ran.
type EmbedConfig struct {
	// none | gemini | openai. Defaults to following AI_PROVIDER, since if you
	// have configured one you almost certainly want the other.
	Provider string `env:"EMBED_PROVIDER"`
	APIKey   string `env:"EMBED_API_KEY"`
	// Both defaults produce 768 dimensions, which is what the schema is fixed at:
	// gemini text-embedding-004 natively, openai text-embedding-3-small via its
	// dimensions parameter.
	Model   string        `env:"EMBED_MODEL"`
	BaseURL string        `env:"EMBED_BASE_URL"`
	Timeout time.Duration `env:"EMBED_TIMEOUT, default=30s"`
	// Drops weak matches. Cosine similarity always returns something, so without
	// a floor a search for "dentists" cheerfully returns bakeries, ranked.
	MinSimilarity float64 `env:"EMBED_MIN_SIMILARITY, default=0.3"`
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
	Concurrency int           `env:"WORKER_CONCURRENCY, default=4"`
	JobTimeout  time.Duration `env:"WORKER_JOB_TIMEOUT, default=2m"`
	// A collection job drives a headless browser over Google Maps for minutes on
	// end. The ordinary 2m job timeout would kill every Maps scrape before it
	// produced a single row, so collection gets its own, much longer budget.
	CollectTimeout time.Duration `env:"WORKER_COLLECT_TIMEOUT, default=30m"`
	MinJobDelay    time.Duration `env:"WORKER_MIN_JOB_DELAY, default=500ms"`
	MaxJobDelay    time.Duration `env:"WORKER_MAX_JOB_DELAY, default=2s"`
	ShutdownWait   time.Duration `env:"WORKER_SHUTDOWN_WAIT, default=30s"`
}

type SourcesConfig struct {
	GooglePlacesAPIKey string `env:"GOOGLE_PLACES_API_KEY"`
	CSVDir             string `env:"CSV_IMPORT_DIR, default=data"`
}

// GmapsConfig drives gosom/google-maps-scraper, which scrapes Google Maps with
// a headless Chromium and needs no API key.
type GmapsConfig struct {
	Enabled bool `env:"GMAPS_ENABLED, default=true"`
	// binary (a google-maps-scraper on PATH) or docker (the published image).
	Mode   string `env:"GMAPS_MODE, default=docker"`
	Binary string `env:"GMAPS_BINARY, default=google-maps-scraper"`
	// The -rod tag, not :latest. Every Playwright-based tag is currently broken:
	// they pin a driver version that only existed on playwright.azureedge.net,
	// which Microsoft has retired, so the container dies on startup with
	// "could not install driver ... 404". The -rod build drives Chromium over
	// CDP with go-rod and downloads no driver, so it just works.
	DockerImage string `env:"GMAPS_DOCKER_IMAGE, default=gosom/google-maps-scraper:latest-rod"`

	// Each unit of concurrency is a headless Chromium. gosom's own Kubernetes
	// example asks for 512Mi per instance, so on a 2-core / 7GB box this stays
	// at 1: raising it is the fastest way to push the machine into swap.
	Concurrency int `env:"GMAPS_CONCURRENCY, default=1"`
	// How far the Maps results list is scrolled. Each extra level is roughly
	// another page of businesses, and a lot more browser work.
	Depth int    `env:"GMAPS_DEPTH, default=2"`
	Lang  string `env:"GMAPS_LANG, default=en"`

	// gosom waits for more work rather than exiting, so without this the job
	// would hang until our own timeout killed it.
	ExitOnInactivity time.Duration `env:"GMAPS_EXIT_ON_INACTIVITY, default=2m"`
	// A hard ceiling on one scrape. A browser-driven crawl is slow; on a small
	// machine, give it room.
	Timeout time.Duration `env:"GMAPS_TIMEOUT, default=20m"`

	// gosom can visit each business's website to pull emails. It roughly doubles
	// the work, and our own crawler already does this, so it is off by default.
	ExtractEmail bool `env:"GMAPS_EXTRACT_EMAIL, default=false"`

	// Where the scraper's temporary query and result files go. Empty means the
	// system temp directory.
	WorkDir string `env:"GMAPS_WORK_DIR"`

	// HostWorkDir is what WorkDir is called on the Docker *host*.
	//
	// It only matters when the worker itself runs in a container: `docker run -v
	// <path>:/work` is interpreted by the daemon on the host, so passing our own
	// in-container path would bind-mount a directory that does not exist there,
	// and the scraper would start up and find no query file. Setting this makes
	// us translate the path before handing it to docker. Leave it empty when the
	// worker runs natively.
	HostWorkDir string `env:"GMAPS_HOST_WORK_DIR"`
}

func Load(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := envconfig.Process(ctx, &cfg); err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	cfg.Version = version()
	cfg.applyDefaults()

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// applyDefaults fills in the settings that are almost always the same as
// something you have already configured.
func (c *Config) applyDefaults() {
	// If you set up an AI provider, you want embeddings from the same place with
	// the same key. Making people configure it twice is a way to get one of them
	// wrong.
	if c.Embed.Provider == "" {
		c.Embed.Provider = c.AI.Provider
	}
	if c.Embed.APIKey == "" {
		c.Embed.APIKey = c.AI.APIKey
	}
	if c.Embed.BaseURL == "" && c.Embed.Provider == c.AI.Provider {
		c.Embed.BaseURL = c.AI.BaseURL
	}

	// The default model per provider is the one that yields 768 dimensions, which
	// is what the lead_embeddings column is fixed at.
	if c.Embed.Model == "" {
		switch c.Embed.Provider {
		case "gemini":
			c.Embed.Model = "text-embedding-004"
		case "openai":
			c.Embed.Model = "text-embedding-3-small"
		}
	}
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

	switch c.Embed.Provider {
	case "", "none", "gemini", "openai":
	default:
		return fmt.Errorf("unsupported EMBED_PROVIDER %q (want gemini, openai, or none)", c.Embed.Provider)
	}
	if (c.Embed.Provider == "gemini" || c.Embed.Provider == "openai") && c.Embed.APIKey == "" {
		return fmt.Errorf("EMBED_PROVIDER=%s requires EMBED_API_KEY (or AI_API_KEY)", c.Embed.Provider)
	}
	if c.Embed.MinSimilarity < 0 || c.Embed.MinSimilarity > 1 {
		return fmt.Errorf("EMBED_MIN_SIMILARITY must be between 0 and 1, got %v", c.Embed.MinSimilarity)
	}
	if c.Environment == "production" && c.Auth.JWTSecret == "dev-secret-change-in-production" {
		return fmt.Errorf("JWT_SECRET must be set in production")
	}
	return nil
}

func version() string {
	return "0.1.0"
}
