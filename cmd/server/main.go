package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	// "github.com/vpramatarov/micro-blog/internal/api/handlers"
	chiMW "github.com/go-chi/chi/v5/middleware"

	authService "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	docsService "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	postService "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	userService "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authMW "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	observabilityMW "github.com/vpramatarov/micro-blog/internal/api/middleware/observability"
	rbacMW "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	securityMW "github.com/vpramatarov/micro-blog/internal/api/middleware/security"
	postRepository "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacRepository "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	"github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	userRepository "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	_ "modernc.org/sqlite"
)

func main() {
	// cancel resouces
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)

	cfg := config.Load()

	if err := cfg.ValidateForServer(); err != nil {
		log.Fatalf("config: %v", err)
	}

	db, err := sql.Open("sqlite", cfg.DB_STRING)
	if err != nil {
		log.Fatal("Failed to connect to database:", err)
	}
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(time.Hour)
	defer db.Close()

	usersRepo := userRepository.New(db)
	rbacRepo := rbacRepository.New(db)
	tokensRepo := tokens.New(db)
	postsRepo := postRepository.New(db)

	issuer := auth.NewIssuer(cfg.JWTSecret, cfg.JWTAccessTTL, auth.IssuerOptions{
		Issuer:   cfg.JWTIssuer,
		Audience: cfg.JWTAudience,
	})

	encoder, err := shortcode.New()
	if err != nil {
		log.Fatalf("init shortcode encoder: %v", err)
	}

	authSrvc := authService.New(cfg, usersRepo, tokensRepo, issuer, logger)
	usersSrvc := userService.New(cfg, usersRepo, rbacRepo, logger)
	postsSrvc := postService.New(postsRepo, encoder, logger)
	docsSrvc := docsService.New(issuer, logger)

	// Mountable middlewares.
	authMiddleware := authMW.Authenticate(issuer, logger)
	requireAdmin := rbacMW.RequireRole("Admin", logger)

	r := router.New(
		router.Services{
			Auth:  authSrvc,
			Users: usersSrvc,
			Posts: postsSrvc,
			Docs:  docsSrvc,
		},
		router.Middlewares{
			Auth:         authMiddleware,
			RequireAdmin: requireAdmin,
			Global: []func(http.Handler) http.Handler{
				chiMW.RequestID,
				chiMW.RealIP,
				observabilityMW.RequestLogger(logger),
				chiMW.Recoverer,
				securityMW.LimitBody(securityMW.DefaultBodyLimit),
				securityMW.SecurityHeaders(),
				chiMW.Timeout(120 * time.Second),
			},
		},
	)

	host := "http://localhost"
	apiServer := &http.Server{
		Handler: r,
		Addr:    fmt.Sprintf(":%d", cfg.Port),
	}

	log.Printf("Server starting on %v:%d ...", host, cfg.Port)

	go func() {
		if err := apiServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start on host %v: %v", host, err)
		}
	}()

	var wg sync.WaitGroup
	wg.Go(func() {
		<-ctx.Done()
		// allow server to complete any incomming requests and shut down in 10 seconds
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := apiServer.Shutdown(shutdownContext); err != nil {
			log.Fatalf("Server failed to shutdown %v", err)
		}
	})

	wg.Wait()
}
