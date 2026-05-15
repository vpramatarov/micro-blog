package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// MinJWTSecretBytes is the lower bound on JWT_SECRET length for HS256, which nominally targets 256-bit security. Shorter keys are brute-forceable offline.
const MinJWTSecretBytes = 32

type Config struct {
	Port                int
	DB_STRING           string
	ADMIN_SEED_PASSWORD string
	JWTSecret           string
	JWTAccessTTL        time.Duration
	JWTRefreshTTL       time.Duration
	JWTIssuer           string
	JWTAudience         string
	CookieSecure        bool
}

func Load() *Config {
	// Match Linux/macOS (".test", "/_test/") and Windows (".test.exe", "\_test\").
	isTest := strings.Contains(os.Args[0], ".test") || strings.Contains(os.Args[0], "_test")

	if isTest {
		log.Println("Test mode detected! Loading .env.test...")
		_ = godotenv.Load(".env.test", "../.env.test", "../../.env.test", "../../../.env.test")
	} else {
		if err := godotenv.Load(); err != nil {
			log.Println("No .env file found, using system environment variables")
		}
	}

	secret := getEnv("JWT_SECRET", "")
	if secret == "" {
		log.Println("WARNING: JWT_SECRET is empty; tokens will be signed with an insecure default")
	}

	// SQLite pragmas, all per-connection so the URI form applies them on every pooled conn: foreign_keys enforces ON DELETE CASCADE; journal_mode=WAL
	// unblocks readers during writes; busy_timeout(5000) makes contending writers wait 5s instead of erroring with SQLITE_BUSY.
	dsn := getEnv("DB_STRING", "")
	if dsn != "" {
		dsn = "file:" + dsn + "?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	}

	return &Config{
		Port:                getEnvAsInt("PORT", 8080),
		DB_STRING:           dsn,
		ADMIN_SEED_PASSWORD: getEnv("ADMIN_SEED_PASSWORD", "changeme"),
		JWTSecret:           secret,
		JWTAccessTTL:        getEnvAsDuration("JWT_ACCESS_TTL", 15*time.Minute),
		JWTRefreshTTL:       getEnvAsDuration("JWT_REFRESH_TTL", 7*24*time.Hour),
		JWTIssuer:           getEnv("JWT_ISSUER", "micro-blog"),
		JWTAudience:         getEnv("JWT_AUDIENCE", "micro-blog-api"),
		CookieSecure:        getEnvAsBool("COOKIE_SECURE", true),
	}
}

// ValidateForServer returns an error if the config is unsafe for serving
// authenticated traffic. Called from the API server's startup path; the
// migrate CLI deliberately does not call this — it only needs DB_STRING.
func (c *Config) ValidateForServer() error {
	if c.JWTSecret == "" {
		return fmt.Errorf("JWT_SECRET is required; set a random value of at least %d bytes", MinJWTSecretBytes)
	}

	if len(c.JWTSecret) < MinJWTSecretBytes {
		return fmt.Errorf("JWT_SECRET is too short (%d bytes); HS256 needs at least %d bytes of entropy",
			len(c.JWTSecret), MinJWTSecretBytes)
	}

	if c.JWTIssuer == "" {
		return fmt.Errorf("JWT_ISSUER is required")
	}

	if c.JWTAudience == "" {
		return fmt.Errorf("JWT_AUDIENCE is required")
	}

	return nil
}

func getEnv(key, defaultVal string) string {
	if value, exists := os.LookupEnv(key); exists {
		return value
	}

	return defaultVal
}

func getEnvAsInt(key string, defaultVal int) int {
	valueStr := getEnv(key, "")
	if value, err := strconv.Atoi(valueStr); err == nil {
		return value
	}

	return defaultVal
}

func getEnvAsDuration(key string, defaultVal time.Duration) time.Duration {
	valueStr := getEnv(key, "")
	if value, err := time.ParseDuration(valueStr); err == nil {
		return value
	}

	return defaultVal
}

func getEnvAsBool(key string, defaultVal bool) bool {
	valueStr := getEnv(key, "")
	if value, err := strconv.ParseBool(valueStr); err == nil {
		return value
	}

	return defaultVal
}
