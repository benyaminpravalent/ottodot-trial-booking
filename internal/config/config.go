// Package config reads runtime configuration from the environment, loading a local
// .env file first if present (never overriding variables already set in the environment).
package config

import (
	"errors"
	"os"

	"github.com/joho/godotenv"
)

// Config is everything the binaries need.
type Config struct {
	DatabaseURL string
	Port        string
}

// Load reads .env (if any) and then the environment.
func Load() (Config, error) {
	_ = godotenv.Load() // missing .env is fine; docker/CI set real env vars

	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"),
		Port:        os.Getenv("PORT"),
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is not set (copy .env.example to .env)")
	}
	if c.Port == "" {
		c.Port = "3000"
	}
	return c, nil
}
