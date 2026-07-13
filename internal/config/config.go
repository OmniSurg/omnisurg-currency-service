// Package config defines the currency service configuration loaded from the
// environment via omnisurg-go-common/config. There is no KEK: the service
// stores no encrypted PII.
package config

import common "github.com/OmniSurg/omnisurg-go-common/config"

// Config holds every tunable.
type Config struct {
	Env      string `envconfig:"OMNISURG_ENV" default:"local"`
	HTTPPort int    `envconfig:"OMNISURG_HTTP_PORT" default:"8092"`
	GRPCPort int    `envconfig:"OMNISURG_GRPC_PORT" default:"9092"`
	LogLevel string `envconfig:"OMNISURG_LOG_LEVEL" default:"info"`

	SentryDSN string `envconfig:"OMNISURG_SENTRY_DSN"`

	DatabaseURL string `envconfig:"OMNISURG_DATABASE_URL" required:"true"`
	JWTSecret   string `envconfig:"OMNISURG_JWT_SECRET" required:"true"`

	CORSOrigins []string `envconfig:"OMNISURG_CORS_ORIGINS" default:"http://localhost:5173"`

	// Redis caches the latest rate per pair for the cache TTL window. When unset
	// or unreachable the service degrades gracefully to the database.
	RedisAddr     string `envconfig:"OMNISURG_REDIS_ADDR" default:"localhost:6379"`
	RedisPassword string `envconfig:"OMNISURG_REDIS_PASSWORD" default:""`
	RedisDB       int    `envconfig:"OMNISURG_REDIS_DB" default:"0"`
	// RateCacheTTLSeconds is the latest-rate cache lifetime. Matches the
	// x-nfr cache_ttl_seconds on getLatestRate.
	RateCacheTTLSeconds int `envconfig:"OMNISURG_RATE_CACHE_TTL_SECONDS" default:"60"`

	// ZimRateURL is the upstream rate source. In the local stack this is the
	// zimrate-stub. The path is appended by the client.
	ZimRateURL string `envconfig:"OMNISURG_ZIMRATE_URL" default:"http://localhost:9003"`
	// ZimRateAPIKey is the Bearer token for the real ZimRate API (free tier).
	// Empty against the local stub.
	ZimRateAPIKey string `envconfig:"OMNISURG_ZIMRATE_API_KEY" default:""`
}

// Load reads the optional dotenv file then populates Config from the env.
func Load(dotenvPath string) (Config, error) {
	var cfg Config
	if err := common.Load(&cfg, dotenvPath); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// IsProduction reports whether this is the production environment.
func (c Config) IsProduction() bool { return c.Env == "production" }

// IsLocal reports whether this is the local environment.
func (c Config) IsLocal() bool { return c.Env == "local" }
