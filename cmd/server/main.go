package main

import (
	"context"
	"flag"
	"log/slog"
	"os"

	"github.com/redis/go-redis/v9"

	"github.com/makeforme/leadscraper/internal/adapters/api"
	"github.com/makeforme/leadscraper/internal/queue"
	"github.com/makeforme/leadscraper/internal/workers"
	"github.com/makeforme/leadscraper/pkg/config"
	"github.com/makeforme/leadscraper/pkg/logger"
)

func main() {
	var isWorker bool
	flag.BoolVar(&isWorker, "worker", false, "start in worker mode")
	flag.Parse()

	ctx := context.Background()

	cfg, err := config.Load(ctx)
	if err != nil {
		slog.Error("failed to load config", "error", err)
		os.Exit(1)
	}

	log := logger.New(cfg.LogLevel, cfg.Environment)
	slog.SetDefault(log)

	log.Info("starting leadscraper",
		"version", cfg.Version,
		"environment", cfg.Environment,
		"mode", map[bool]string{true: "worker", false: "api"}[isWorker],
	)

	rdb := redis.NewClient(&redis.Options{
		Addr:     redisAddr(cfg.Redis.URL),
		Password: cfg.Redis.Password,
		DB:       0,
	})

	if isWorker {
		q := queue.NewRedisQueue(rdb, "leadscraper:jobs")
		w := workers.NewWorker(q, log)
		if err := w.Start(ctx); err != nil {
			log.Error("worker error", "error", err)
			os.Exit(1)
		}
		return
	}

	router := api.NewRouter(cfg.Version)
	srv := api.NewServer(cfg.Port, router, log)

	if err := srv.Start(ctx); err != nil {
		log.Error("server error", "error", err)
		os.Exit(1)
	}
}

func redisAddr(url string) string {
	if url == "" {
		return "localhost:6379"
	}
	return url
}
