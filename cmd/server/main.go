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

	chiMW "github.com/go-chi/chi/v5/middleware"

	authService "github.com/vpramatarov/micro-blog/internal/api/handlers/auth"
	categoryService "github.com/vpramatarov/micro-blog/internal/api/handlers/categories"
	docsService "github.com/vpramatarov/micro-blog/internal/api/handlers/docs"
	postService "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	shortLinkService "github.com/vpramatarov/micro-blog/internal/api/handlers/shortlinks"
	tagService "github.com/vpramatarov/micro-blog/internal/api/handlers/tags"
	userService "github.com/vpramatarov/micro-blog/internal/api/handlers/users"
	authMW "github.com/vpramatarov/micro-blog/internal/api/middleware/auth"
	observabilityMW "github.com/vpramatarov/micro-blog/internal/api/middleware/observability"
	rbacMW "github.com/vpramatarov/micro-blog/internal/api/middleware/rbac"
	securityMW "github.com/vpramatarov/micro-blog/internal/api/middleware/security"
	categoriesRepository "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	"github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
	postRepository "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	rbacRepository "github.com/vpramatarov/micro-blog/internal/api/repository/rbac"
	shortLinksRepository "github.com/vpramatarov/micro-blog/internal/api/repository/shortlinks"
	tagRepository "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	"github.com/vpramatarov/micro-blog/internal/api/repository/tokens"
	userRepository "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/api/router"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/imagex"
	jobsWorker "github.com/vpramatarov/micro-blog/internal/jobs"
	"github.com/vpramatarov/micro-blog/internal/shortcode"
	"github.com/vpramatarov/micro-blog/internal/uploads"
	_ "modernc.org/sqlite"
)

// uploadsDir is where post featured images and their variants live on disk.
// Relative to the server's CWD; the same value is passed to the static-file handler so /uploads/* serves what the storage layer wrote.
const uploadsDir string = "./uploads"

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
	shortLinksRepo := shortLinksRepository.New(db)
	categoriesRepo := categoriesRepository.New(db)
	tagsRepo := tagRepository.New(db)
	jobsRepo := jobs.New(db)

	storage := uploads.New(uploadsDir)

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
	postsSrvc := postService.New(postsRepo, categoriesRepo, tagsRepo, storage, jobsRepo, encoder, logger)
	docsSrvc := docsService.New(issuer, logger)
	shortLinksSrvc := shortLinkService.New(shortLinksRepo, encoder, logger)
	categorySrvc := categoryService.New(categoriesRepo, logger)
	tagSrvc := tagService.New(tagsRepo, logger)

	// Job worker — recovers any stuck 'running' rows from a previous crash, then polls forever.
	// The worker's context is the same SIGINT/SIGTERM context the HTTP server uses, so Ctrl+C stops both cleanly.
	if n, err := jobsRepo.ResetStuckRunning(ctx); err != nil {
		logger.Warn("reset stuck jobs", "err", err)
	} else if n > 0 {
		logger.Info("requeued stuck jobs", "count", n)
	}

	worker := jobsWorker.NewWorker(jobsRepo, logger)
	worker.Register("image_variants", imagex.NewVariantsHandler(storage, logger))
	go worker.Run(ctx)

	// Mountable middlewares.
	authMiddleware := authMW.Authenticate(issuer, logger)
	requireAdmin := rbacMW.RequireRole("Admin", logger)
	requireAdminOrEditor := rbacMW.RequireAnyRole(logger, "Admin", "Editor")

	r := router.New(
		router.Services{
			Auth:       authSrvc,
			Users:      usersSrvc,
			Posts:      postsSrvc,
			ShortLinks: shortLinksSrvc,
			Categories: categorySrvc,
			Tags:       tagSrvc,
			Docs:       docsSrvc,
		},
		router.Middlewares{
			Auth:                 authMiddleware,
			RequireAdmin:         requireAdmin,
			RequireEditorOrAdmin: requireAdminOrEditor,
			Global: []func(http.Handler) http.Handler{
				chiMW.RequestID,
				chiMW.RealIP,
				observabilityMW.RequestLogger(logger),
				chiMW.Recoverer,
				securityMW.LimitBody(securityMW.DefaultBodyLimit),
				securityMW.SecurityHeaders(securityMW.Options{EnableHSTS: cfg.CookieSecure}),
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
