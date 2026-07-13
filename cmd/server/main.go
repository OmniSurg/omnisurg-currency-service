package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/OmniSurg/omnisurg-currency-service/internal/cache"
	"github.com/OmniSurg/omnisurg-currency-service/internal/client"
	"github.com/OmniSurg/omnisurg-currency-service/internal/config"
	"github.com/OmniSurg/omnisurg-currency-service/internal/grpcserver"
	"github.com/OmniSurg/omnisurg-currency-service/internal/handler"
	"github.com/OmniSurg/omnisurg-currency-service/internal/repository"
	"github.com/OmniSurg/omnisurg-currency-service/internal/service"
	"github.com/OmniSurg/omnisurg-go-common/logger"
	mw "github.com/OmniSurg/omnisurg-go-common/middleware"
	pg "github.com/OmniSurg/omnisurg-go-common/postgres"
	currencyv1 "github.com/OmniSurg/omnisurg-proto/gen/go/omnisurg/currency/v1"
	"github.com/getsentry/sentry-go"
	"github.com/redis/go-redis/v9"
	"github.com/rs/zerolog"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load(".env")
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	baseLogger := logger.New(logger.Options{Service: "currency-service", Level: level, Writer: os.Stdout, Production: cfg.IsProduction()})

	if cfg.SentryDSN != "" {
		if serr := sentry.Init(sentry.ClientOptions{Dsn: cfg.SentryDSN, Environment: cfg.Env}); serr != nil {
			baseLogger.Error().Err(serr).Msg("sentry init failed")
		} else {
			defer sentry.Flush(2 * time.Second)
		}
	}

	if merr := runMigrations(cfg.DatabaseURL); merr != nil {
		return fmt.Errorf("migrations: %w", merr)
	}

	ctx := context.Background()
	pool, err := pg.OpenPool(ctx, pg.Options{DSN: cfg.DatabaseURL})
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	// Redis is best effort: a connection failure logs a warning and the service
	// continues with a nil cache client. Conversion and rate reads fall back to
	// the database, so a redis outage never takes the service down.
	var redisClient *redis.Client
	if cfg.RedisAddr != "" {
		rc := redis.NewClient(&redis.Options{Addr: cfg.RedisAddr, Password: cfg.RedisPassword, DB: cfg.RedisDB})
		pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		if perr := rc.Ping(pingCtx).Err(); perr != nil {
			baseLogger.Warn().Err(perr).Str("addr", cfg.RedisAddr).Msg("redis unavailable, serving rates from the database only")
			_ = rc.Close()
		} else {
			redisClient = rc
			defer func() { _ = rc.Close() }()
		}
		cancel()
	}
	rateCache := cache.New(redisClient, time.Duration(cfg.RateCacheTTLSeconds)*time.Second)

	snapshotRepo := repository.NewSnapshotRepository(pool)
	currencyRepo := repository.NewCurrencyRepository(pool)
	configRepo := repository.NewConfigRepository(pool)
	auditRepo := repository.NewAuditRepository(pool)
	zimrate := client.NewZimRateClient(cfg.ZimRateURL, cfg.ZimRateAPIKey)

	ratesSvc := service.NewCurrencyService(snapshotRepo, currencyRepo, rateCache, zimrate, auditRepo)
	configSvc := service.NewConfigService(configRepo, currencyRepo, auditRepo)

	router := handler.NewRouter(handler.RouterConfig{
		Rates: ratesSvc, Config: configSvc, Audit: auditRepo,
		JWTSecret: cfg.JWTSecret, Env: cfg.Env, BaseLogger: baseLogger,
		CORSOrigins: cfg.CORSOrigins, Ping: pool.Ping,
	})

	httpSrv := &http.Server{
		Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: router,
		ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
	}

	// The business server registers on the SAME grpc.Server as the health
	// server, behind the shared go-common interceptor (verifies any forwarded
	// JWT, propagates the request id, skips the health prefix). currency-service
	// is NOT tenant-scoped: fx_snapshots and currencies are platform-global no-RLS
	// tables, so RequireTenant is false. The FX read is the 50 ms p99 hop billing
	// depends on; only GetLatestRate and Convert are exposed on gRPC (Refresh and
	// SetManualRate stay REST-only).
	grpcSrv := grpc.NewServer(
		grpc.UnaryInterceptor(mw.UnaryServerInterceptor(mw.InterceptorOptions{
			JWTSecret:     cfg.JWTSecret,
			RequireTenant: false,
		})),
	)
	healthSrv := health.NewServer()
	healthpb.RegisterHealthServer(grpcSrv, healthSrv)
	healthSrv.SetServingStatus("currency-service", healthpb.HealthCheckResponse_SERVING)

	currencyv1.RegisterCurrencyServiceServer(grpcSrv, grpcserver.New(ratesSvc))

	// Reflection eases gRPC smoke probes and local debugging. Disabled in
	// production, mirroring the non production only Swagger UI policy.
	if !cfg.IsProduction() {
		reflection.Register(grpcSrv)
	}

	errCh := make(chan error, 2)
	go func() {
		baseLogger.Info().Int("port", cfg.HTTPPort).Msg("http server listening")
		if serveErr := httpSrv.ListenAndServe(); serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- fmt.Errorf("http serve: %w", serveErr)
		}
	}()
	go func() {
		lis, lerr := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
		if lerr != nil {
			errCh <- fmt.Errorf("grpc listen: %w", lerr)
			return
		}
		baseLogger.Info().Int("port", cfg.GRPCPort).Msg("grpc server listening (health plus currency business)")
		if serveErr := grpcSrv.Serve(lis); serveErr != nil {
			errCh <- fmt.Errorf("grpc serve: %w", serveErr)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case sig := <-stop:
		baseLogger.Info().Str("signal", sig.String()).Msg("shutting down")
	case serveErr := <-errCh:
		return serveErr
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}
