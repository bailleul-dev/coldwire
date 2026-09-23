// Package config loads the application settings.
package config

import "os"

type Config struct {
	Addr string // listen address
	DSN  string // database connection string

	// UserStore selects the driven adapter for users: "sql" or "memory".
	UserStore string
}

// Load reads the settings from the environment, with local defaults.
func Load() Config {
	return Config{
		Addr: getenv("ADDR", ":8080"),
		DSN:  getenv("DATABASE_URL", "demo://users"),

		UserStore: getenv("USER_STORE", "sql"),
	}
}

func getenv(key, def string) string {
	if v, ok := os.LookupEnv(key); ok {
		return v
	}
	return def
}
