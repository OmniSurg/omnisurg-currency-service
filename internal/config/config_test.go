package config_test

import (
	"os"
	"testing"

	"github.com/OmniSurg/omnisurg-currency-service/internal/config"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func setRequired(t *testing.T) {
	t.Setenv("OMNISURG_DATABASE_URL", "postgres://u:p@localhost:5432/omnisurg_currency?sslmode=disable")
	t.Setenv("OMNISURG_JWT_SECRET", "secret")
}

func TestLoadAppliesDefaults(t *testing.T) {
	setRequired(t)
	cfg, err := config.Load("")
	require.NoError(t, err)
	assert.Equal(t, 8092, cfg.HTTPPort)
	assert.Equal(t, 9092, cfg.GRPCPort)
	assert.Equal(t, "local", cfg.Env)
	assert.True(t, cfg.IsLocal())
	assert.Equal(t, 60, cfg.RateCacheTTLSeconds)
	assert.Equal(t, "http://localhost:9003", cfg.ZimRateURL)
}

func TestLoadFailsWithoutDatabaseURL(t *testing.T) {
	t.Setenv("OMNISURG_JWT_SECRET", "secret")
	os.Unsetenv("OMNISURG_DATABASE_URL")
	_, err := config.Load("")
	require.Error(t, err)
}

func TestLoadFailsWithoutJWTSecret(t *testing.T) {
	t.Setenv("OMNISURG_DATABASE_URL", "postgres://u:p@localhost:5432/db?sslmode=disable")
	os.Unsetenv("OMNISURG_JWT_SECRET")
	_, err := config.Load("")
	require.Error(t, err)
}
