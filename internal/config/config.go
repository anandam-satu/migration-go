// Package config mirrors the settings previously declared in
// src/main/resources/application.properties. Every value can be overridden
// with the same environment variables the Spring Boot app used.
package config

import (
	"os"
	"strconv"
	"time"
)

// MySQL holds connection/pool settings for the legacy MyBiz database
// (spring.datasource.mysql.* in the original application.properties).
type MySQL struct {
	Enabled     bool
	Host        string
	Port        string
	DBName      string
	User        string
	Password    string
	MaxOpen     int
	MaxIdle     int
	ConnTimeout time.Duration
	IdleTimeout time.Duration
	MaxLifetime time.Duration
}

// Postgres holds connection/pool settings for the main "stok-anandam"
// database (spring.datasource.pg.*).
type Postgres struct {
	Host        string
	Port        string
	DBName      string
	User        string
	Password    string
	MaxOpen     int
	MaxIdle     int
	MaxLifetime time.Duration
}

// Config is the root runtime configuration.
type Config struct {
	MySQL        MySQL
	Postgres     Postgres
	LegacySchema string // migration.legacy-schema (informational; SQL is ported verbatim)
	Port         string // server.port
}

func env(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

// Load reads the configuration from the environment. Defaults match the
// original application.properties exactly.
func Load() Config {
	return Config{
		MySQL: MySQL{
			Enabled:     envBool("APP_MYSQL_ENABLED", true),
			Host:        env("DB_MYSQL_HOST", "192.168.1.246"),
			Port:        env("DB_MYSQL_PORT", "3307"),
			DBName:      env("DB_MYSQL_NAME", "anandamid26"),
			User:        env("DB_MYSQL_USER", "root"),
			Password:    os.Getenv("DB_MYSQL_PASSWORD"), // default: empty
			MaxOpen:     5,
			MaxIdle:     0, // minimum-idle=0 (see app docs: avoid Sleep conns on MyBiz)
			ConnTimeout: 60 * time.Second,
			IdleTimeout: 60 * time.Second,
			MaxLifetime: 30 * time.Minute,
		},
		Postgres: Postgres{
			Host:        env("DB_PG_HOST", "192.168.1.176"),
			Port:        env("DB_PG_PORT", "5432"),
			DBName:      env("DB_PG_NAME", "stok-anandam"),
			User:        env("DB_PG_USER", "anandamstok"),
			Password:    env("DB_PG_PASSWORD", "Letmein99+"),
			MaxOpen:     10, // hikari.maximum-pool-size
			MaxIdle:     10, // Hikari default minimum-idle == maximum-pool-size
			MaxLifetime: 30 * time.Minute,
		},
		LegacySchema: env("MIGRATION_LEGACY_SCHEMA", "anandamid26"),
		Port:         env("SERVER_PORT", "9089"),
	}
}
