package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/brianvoe/gofakeit/v7"

	categoriesrepo "github.com/vpramatarov/micro-blog/internal/api/repository/categories"
	postsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/posts"
	tagsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/tags"
	usersrepo "github.com/vpramatarov/micro-blog/internal/api/repository/users"
	"github.com/vpramatarov/micro-blog/internal/auth"
	"github.com/vpramatarov/micro-blog/internal/config"
	"github.com/vpramatarov/micro-blog/internal/markdown"
	"github.com/vpramatarov/micro-blog/internal/slug"
)

// Role IDs mirror migration 00004 (1=Admin, 2=Editor, 3=Author, 4=Subscriber).
const (
	roleEditor     = 2
	roleAuthor     = 3
	roleSubscriber = 4
)

var postStatuses = []string{"published", "draft", "archived"}

// seedUser pairs a created user's login identity with its role for the end-of-run credential printout.
type seedUser struct {
	email string
	role  string
}

// runSeedDemo populates the DB with realistic dev/test fixtures: users across
// roles, categories, tags, posts, and post↔tag links. It reuses the same repo +
// slug + markdown logic the HTTP handlers use, so seeded rows are shaped exactly
// like API-created ones. Dev/test only — refuses under GO_ENV=prod unless -force.
func runSeedDemo(db *sql.DB, cfg *config.Config, argv []string) {
	fs := flag.NewFlagSet("seed-demo", flag.ExitOnError)
	reset := fs.Bool("reset", false, "wipe demo content (and non-admin users) before seeding")
	force := fs.Bool("force", false, "allow running when GO_ENV=prod")
	seed := fs.Uint64("seed", 11, "RNG seed for reproducible data")
	password := fs.String("password", "password123", "shared login password for every seeded demo user")
	nEditors := fs.Int("editors", 1, "number of Editor users to create")
	nAuthors := fs.Int("authors", 3, "number of Author users to create")
	nSubscribers := fs.Int("subscribers", 2, "number of Subscriber users to create")
	nCategories := fs.Int("categories", 5, "number of categories to create")
	nTags := fs.Int("tags", 12, "number of tags to create")
	nPosts := fs.Int("posts", 15, "number of posts to create")
	_ = fs.Parse(argv)

	if cfg.Env == "prod" && !*force {
		log.Fatal("seed-demo refuses to run with GO_ENV=prod; pass -force only if you really mean it")
	}

	ctx := context.Background()
	f := gofakeit.New(*seed)

	if *reset {
		if err := wipeDemoData(db); err != nil {
			log.Fatalf("reset: %v", err)
		}
		fmt.Println("Reset: wiped demo content (kept admin, RBAC, Uncategorized).")
	}

	usersRepo := usersrepo.New(db)
	catsRepo := categoriesrepo.New(db)
	tagsRepo := tagsrepo.New(db)
	postsRepo := postsrepo.New(db)
	hash, err := auth.Hash(*password)
	if err != nil {
		log.Fatalf("hash demo password: %v", err)
	}

	// --- Users
	created := make([]seedUser, 0, *nEditors+*nAuthors+*nSubscribers)
	var authorIDs []int64 // users who can own posts (Editors + Authors + admin)

	mkUsers := func(count int, roleID int64, roleName string, authorCapable bool) {
		for i := 0; i < count; i++ {
			username := uniqueUsername(f)
			email := username + "@demo.test"
			id, err := usersRepo.Create(ctx, username, email, hash, roleID)
			if err != nil {
				if errors.Is(err, usersrepo.ErrUserDuplicate) {
					continue // extremely unlikely after a unique-username pass; skip
				}

				log.Fatalf("create %s user: %v", roleName, err)
			}

			created = append(created, seedUser{email: email, role: roleName})
			if authorCapable {
				authorIDs = append(authorIDs, id)
			}
		}
	}

	mkUsers(*nEditors, roleEditor, "Editor", true)
	mkUsers(*nAuthors, roleAuthor, "Author", true)
	mkUsers(*nSubscribers, roleSubscriber, "Subscriber", false)

	// Include the existing admin (seeded separately) in the author pool so some
	// posts are owned by it.
	if adminID, ok := lookupAdminID(ctx, db); ok {
		authorIDs = append(authorIDs, adminID)
	}

	if len(authorIDs) == 0 {
		log.Fatal("no author-capable users to attach posts to (create at least one editor/author, or seed the admin first)")
	}

	// --- Categories
	// Pool starts with the always-present Uncategorized (id 1).
	categoryIDs := []int64{1}
	for _, name := range distinctNames(f, *nCategories, func() string { return cases(f.Hobby()) }) {
		id, err := slug.Allocate(ctx, catsRepo, name, "", 0,
			func(s string) (int64, error) { return catsRepo.Create(ctx, name, s) })
		if err != nil {
			if errors.Is(err, categoriesrepo.ErrCategoryDuplicate) {
				continue
			}

			log.Fatalf("create category %q: %v", name, err)
		}

		categoryIDs = append(categoryIDs, id)
	}

	// --- Tags
	var tagIDs []int64
	for _, name := range distinctNames(f, *nTags, func() string { return strings.ToLower(f.BuzzWord()) }) {
		id, err := slug.Allocate(ctx, tagsRepo, name, "", 0,
			func(s string) (int64, error) { return tagsRepo.Create(ctx, name, s) })
		if err != nil {
			if errors.Is(err, tagsrepo.ErrTagDuplicate) {
				continue
			}

			log.Fatalf("create tag %q: %v", name, err)
		}

		tagIDs = append(tagIDs, id)
	}

	// --- Posts
	now := time.Now().UTC()
	earliest := now.AddDate(0, 0, -60)
	for i := 0; i < *nPosts; i++ {
		title := strings.TrimRight(f.Sentence(f.IntRange(4, 8)), ".")
		body := "## " + strings.TrimRight(f.Sentence(f.IntRange(3, 6)), ".") + "\n\n" +
			f.Paragraph(f.IntRange(2, 4), f.IntRange(3, 6), 14, "\n\n")
		html, err := markdown.Render(body)
		if err != nil {
			log.Fatalf("render markdown: %v", err)
		}

		authorID := authorIDs[f.IntRange(0, len(authorIDs)-1)]
		categoryID := categoryIDs[f.IntRange(0, len(categoryIDs)-1)]
		status := weightedStatus(f)
		postID, err := slug.Allocate(ctx, postsRepo, title, "", 0,
			func(s string) (int64, error) {
				return postsRepo.Create(ctx, postsrepo.PostInsert{
					AuthorID: authorID, CategoryID: categoryID,
					Title: title, Markdown: body, HTML: html, Slug: s,
					FeaturedImagePath: "", Status: status,
				})
			})
		if err != nil {
			log.Fatalf("create post %q: %v", title, err)
		}

		if len(tagIDs) > 0 {
			picked := pickTags(f, tagIDs, f.IntRange(1, 4))
			if err := tagsRepo.ReplaceForPost(ctx, postID, picked); err != nil {
				log.Fatalf("attach tags to post %d: %v", postID, err)
			}
		}

		// Spread created_at over the last ~60 days for a realistic ordering (Create itself stamps the DB default = now).
		ts := f.DateRange(earliest, now).Format("2006-01-02 15:04:05")
		if _, err := db.ExecContext(ctx, "UPDATE posts SET created_at = ? WHERE id = ?", ts, postID); err != nil {
			log.Fatalf("set created_at on post %d: %v", postID, err)
		}
	}

	// --- Summary
	fmt.Printf("\nSeed complete (seed=%d): %d users, %d categories (+Uncategorized), %d tags, %d posts.\n",
		*seed, len(created), len(categoryIDs)-1, len(tagIDs), *nPosts)
	fmt.Printf("\nDemo logins (password: %q):\n", *password)
	for _, u := range created {
		fmt.Printf("  %-28s  %s\n", u.email, u.role)
	}

	fmt.Println()
}

// wipeDemoData clears demo content child→parent (respecting posts.category_id ON DELETE RESTRICT) while preserving the admin user, RBAC reference rows,
// and the Uncategorized category (id 1). Non-admin users cascade their refresh_tokens/short_links via FK.
func wipeDemoData(db *sql.DB) error {
	stmts := []string{
		"DELETE FROM post_tags",
		"DELETE FROM posts",
		"DELETE FROM categories WHERE id <> 1",
		"DELETE FROM tags",
		"DELETE FROM short_links",
		"DELETE FROM users WHERE role_id <> 1",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			return fmt.Errorf("%s: %w", s, err)
		}
	}

	return nil
}

func lookupAdminID(ctx context.Context, db *sql.DB) (int64, bool) {
	var id int64
	err := db.QueryRowContext(ctx, "SELECT id FROM users WHERE role_id = 1 ORDER BY id LIMIT 1").Scan(&id)
	if err != nil {
		return 0, false
	}

	return id, true
}

// uniqueUsername generates a username and lowercases/strips it to the charset
// the validators accept; gofakeit usernames are already simple, but we guard.
func uniqueUsername(f *gofakeit.Faker) string {
	u := strings.ToLower(f.Username())
	u = strings.NewReplacer(".", "", " ", "").Replace(u)
	if len(u) < 3 {
		u = u + "user"
	}

	return u
}

// distinctNames returns n unique names produced by gen, retrying on collisions
// and falling back to a numeric suffix so it always returns n entries.
func distinctNames(f *gofakeit.Faker, n int, gen func() string) []string {
	seen := make(map[string]struct{}, n)
	out := make([]string, 0, n)
	for len(out) < n {
		name := strings.TrimSpace(gen())
		key := strings.ToLower(name)
		if name == "" {
			continue
		}

		if _, dup := seen[key]; dup {
			name = fmt.Sprintf("%s %d", name, f.Number(2, 999))
			key = strings.ToLower(name)
			if _, dup := seen[key]; dup {
				continue
			}
		}

		seen[key] = struct{}{}
		out = append(out, name)
	}

	return out
}

func weightedStatus(f *gofakeit.Faker) string {
	switch n := f.Number(1, 100); {
	case n <= 70:
		return postStatuses[0] // published
	case n <= 90:
		return postStatuses[1] // draft
	default:
		return postStatuses[2] // archived
	}
}

// pickTags returns up to n distinct tag IDs via a partial Fisher–Yates shuffle.
func pickTags(f *gofakeit.Faker, ids []int64, n int) []int64 {
	if n > len(ids) {
		n = len(ids)
	}

	pool := make([]int64, len(ids))
	copy(pool, ids)
	for i := 0; i < n; i++ {
		j := f.IntRange(i, len(pool)-1)
		pool[i], pool[j] = pool[j], pool[i]
	}

	return pool[:n]
}

// cases title-cases a single-word/phrase category name for nicer display.
func cases(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}
