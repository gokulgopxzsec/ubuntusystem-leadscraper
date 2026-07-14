package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"golang.org/x/sync/errgroup"

	"github.com/makeforme/leadscraper/internal/adapters/api"
	"github.com/makeforme/leadscraper/internal/adapters/csv"
	"github.com/makeforme/leadscraper/internal/adapters/db/postgres"
	"github.com/makeforme/leadscraper/internal/adapters/gmaps"
	"github.com/makeforme/leadscraper/internal/adapters/googlemaps"
	"github.com/makeforme/leadscraper/internal/ai"
	"github.com/makeforme/leadscraper/internal/ai/gemini"
	"github.com/makeforme/leadscraper/internal/ai/openai"
	"github.com/makeforme/leadscraper/internal/crawler"
	"github.com/makeforme/leadscraper/internal/ports"
	"github.com/makeforme/leadscraper/internal/queue"
	"github.com/makeforme/leadscraper/internal/scoring"
	"github.com/makeforme/leadscraper/internal/workers"
	"github.com/makeforme/leadscraper/pkg/config"
	"github.com/makeforme/leadscraper/pkg/logger"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run() error {
	// With no flags the process runs the API and the worker together. That makes
	// the whole app one command, and on a small machine one Go process is
	// meaningfully cheaper than two. The split flags exist for when you want to
	// scale the worker separately.
	var (
		workerOnly = flag.Bool("worker", false, "run only the worker")
		apiOnly    = flag.Bool("api", false, "run only the API")
	)
	flag.Parse()

	if *workerOnly && *apiOnly {
		return errors.New("--worker and --api are mutually exclusive; pass neither to run both")
	}
	runAPI, runWorker := !*workerOnly, !*apiOnly

	// Both modes shut down on SIGINT/SIGTERM. The worker previously ran on a
	// background context and could not be stopped at all.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load(ctx)
	if err != nil {
		return err
	}

	log := logger.New(cfg.LogLevel, cfg.Environment)
	slog.SetDefault(log)

	mode := "api+worker"
	switch {
	case !runWorker:
		mode = "api"
	case !runAPI:
		mode = "worker"
	}
	log.Info("starting leadscraper",
		"version", cfg.Version, "environment", cfg.Environment, "mode", mode)

	// ---- Redis ----
	// ParseURL is the whole point here. redis.Options.Addr wants "host:port",
	// so handing it the raw "redis://host:6379/0" URL made every dial fail.
	redisOpts, err := redis.ParseURL(cfg.Redis.URL)
	if err != nil {
		return fmt.Errorf("parse REDIS_URL %q: %w", cfg.Redis.URL, err)
	}
	if cfg.Redis.Password != "" {
		redisOpts.Password = cfg.Redis.Password
	}

	rdb := redis.NewClient(redisOpts)
	defer rdb.Close()

	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connect to redis at %s: %w", redisOpts.Addr, err)
	}
	log.Info("redis connected", "addr", redisOpts.Addr)

	q := queue.NewRedisQueue(rdb, cfg.Redis.QueueKey)

	// ---- Postgres ----
	pool, err := postgres.NewPool(ctx, cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	log.Info("postgres connected")

	if cfg.Database.AutoMigrate {
		if err := postgres.RunMigrations(ctx, pool, cfg.Database.MigrationsDir, log); err != nil {
			return err
		}
	}

	r := newRepos(pool)
	sources := newSources(cfg, log)

	// Both halves share this context, so one Ctrl-C stops the whole app and the
	// worker still gets to drain its in-flight jobs.
	group, gctx := errgroup.WithContext(ctx)

	if runWorker {
		deps := &workers.Deps{
			Businesses:   r.businesses,
			Websites:     r.websites,
			Contacts:     r.contacts,
			Socials:      r.socials,
			Audits:       r.audits,
			Scores:       r.scores,
			Jobs:         r.jobs,
			CrawlResults: r.crawls,
			Technologies: r.techs,

			Sources:    sources,
			Crawler:    crawler.New(cfg.Crawler),
			AI:         newAIProvider(cfg, log),
			Scoring:    scoring.NewDefaultEngine(),
			Queue:      q,
			CrawlerCfg: cfg.Crawler,
			AICfg:      cfg.AI,
		}

		worker := workers.NewWorker(q, deps, cfg.Worker, log)
		group.Go(func() error { return worker.Start(gctx) })
	}

	if runAPI {
		router := api.NewRouter(api.RouterDeps{
			Version:    cfg.Version,
			APIKey:     cfg.Auth.APIKey,
			Pool:       pool,
			Queue:      q,
			Businesses: r.businesses,
			Websites:   r.websites,
			Contacts:   r.contacts,
			Socials:    r.socials,
			Scores:     r.scores,
			Audits:     r.audits,
			Jobs:       r.jobs,
			Sources:    sources,
		})

		srv := api.NewServer(cfg.Port, router, log)
		group.Go(func() error { return srv.Start(gctx) })

		log.Info("dashboard ready", "url", fmt.Sprintf("http://localhost:%d", cfg.Port))
	}

	return group.Wait()
}

type repos struct {
	businesses *postgres.BusinessRepo
	websites   *postgres.WebsiteRepo
	contacts   *postgres.ContactRepo
	socials    *postgres.SocialRepo
	audits     *postgres.AuditRepo
	scores     *postgres.ScoreRepo
	jobs       *postgres.JobRepo
	crawls     *postgres.CrawlResultRepo
	techs      *postgres.TechnologyRepo
}

func newRepos(pool *pgxpool.Pool) *repos {
	return &repos{
		businesses: postgres.NewBusinessRepo(pool),
		websites:   postgres.NewWebsiteRepo(pool),
		contacts:   postgres.NewContactRepo(pool),
		socials:    postgres.NewSocialRepo(pool),
		audits:     postgres.NewAuditRepo(pool),
		scores:     postgres.NewScoreRepo(pool),
		jobs:       postgres.NewJobRepo(pool),
		crawls:     postgres.NewCrawlResultRepo(pool),
		techs:      postgres.NewTechnologyRepo(pool),
	}
}

// newSources registers only the adapters that can actually run. A source that
// would accept jobs and then fail every one of them is worse than one that is
// absent, because /scrape can reject an unknown source up front.
func newSources(cfg *config.Config, log *slog.Logger) map[string]ports.SourceAdapter {
	sources := map[string]ports.SourceAdapter{
		"csv": csv.NewAdapter(cfg.Sources.CSVDir),
	}

	// google_maps scrapes Maps directly through gosom/google-maps-scraper and
	// needs no API key. This is the source that produces real leads.
	if cfg.Gmaps.Enabled {
		gm := gmaps.NewAdapter(cfg.Gmaps, log)
		if err := gm.Available(); err != nil {
			log.Warn("the google_maps source is disabled", "error", err)
		} else {
			sources["google_maps"] = gm
			log.Info("google_maps source ready",
				"mode", cfg.Gmaps.Mode, "concurrency", cfg.Gmaps.Concurrency, "depth", cfg.Gmaps.Depth)
		}
	}

	// google_places is the paid, official API. It is an alternative to scraping,
	// not a requirement, so it stays optional and separately named.
	if cfg.Sources.GooglePlacesAPIKey != "" {
		sources["google_places"] = googlemaps.NewAdapter(cfg.Sources.GooglePlacesAPIKey)
		log.Info("google_places source ready (official API)")
	}

	return sources
}

func newAIProvider(cfg *config.Config, log *slog.Logger) ai.Provider {
	switch cfg.AI.Provider {
	case "gemini":
		log.Info("ai provider enabled", "provider", "gemini", "model", cfg.AI.Model)
		return gemini.NewProvider(cfg.AI.APIKey, cfg.AI.Model, gemini.Options{
			BaseURL:      cfg.AI.BaseURL,
			Timeout:      cfg.AI.Timeout,
			MaxHTMLChars: cfg.AI.MaxHTMLChars,
		})
	case "openai":
		log.Info("ai provider enabled", "provider", "openai", "model", cfg.AI.Model)
		return openai.NewProvider(cfg.AI.APIKey, cfg.AI.Model, openai.Options{
			BaseURL:      cfg.AI.BaseURL,
			Timeout:      cfg.AI.Timeout,
			MaxHTMLChars: cfg.AI.MaxHTMLChars,
		})
	default:
		log.Info("no AI provider configured; leads will be scored by rules only")
		return ai.Noop{}
	}
}
