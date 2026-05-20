package posts_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	postsh "github.com/vpramatarov/micro-blog/internal/api/handlers/posts"
	"github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
	jobsrepo "github.com/vpramatarov/micro-blog/internal/api/repository/jobs"
	"github.com/vpramatarov/micro-blog/internal/imagex"
)

// makeJPEG renders a w×h white JPEG. Big enough by default to pass the MinDimension check; pass smaller values to test rejection.
func makeJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{255, 255, 255, 255})
		}
	}

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatalf("jpeg encode: %v", err)
	}

	return buf.Bytes()
}

func makePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{200, 200, 200, 255})
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("png encode: %v", err)
	}

	return buf.Bytes()
}

func makeGIF(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette{
		color.RGBA{0, 0, 0, 255}, color.RGBA{255, 255, 255, 255},
	})
	var buf bytes.Buffer
	if err := gif.Encode(&buf, img, nil); err != nil {
		t.Fatalf("gif encode: %v", err)
	}

	return buf.Bytes()
}

// runVariantsInline runs the next pending variants job synchronously (no goroutine).
// The image tests assert the on-disk side effects so they need a way to flush the queue without depending on a running worker goroutine.
func runVariantsInline(t *testing.T, env *postWriteEnv) {
	job, err := env.app.jobsWorker.Repo.Claim(context.Background())
	if errors.Is(err, jobsrepo.ErrNoJob) {
		return
	}

	if err != nil {
		t.Fatalf("claim job: %v", err)
	}

	handler := imagex.NewVariantsHandler(env.app.storage, nil)
	if err := handler(context.Background(), job.Payload); err != nil {
		_ = env.app.jobsWorker.Repo.MarkFailed(context.Background(), job.ID, err.Error())
		t.Fatalf("variants handler: %v", err)
	}

	if err := env.app.jobsWorker.Repo.MarkDone(context.Background(), job.ID); err != nil {
		t.Fatalf("mark done: %v", err)
	}
}

// TestCreatePostWithImageHappy covers a JPEG upload through to the variants existing on disk. End-to-end smoke for the whole pipeline.
func TestCreatePostWithImageHappy(t *testing.T) {
	env := setupPostWriteEnv(t)
	jpegBytes := makeJPEG(t, 1024, 1024)
	body := `{"title":"with image","markdown_content":"# with image body content","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "kitten.jpg", jpegBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var got struct {
		ID                int64  `json:"id"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, rec.Body.String())
	}

	if got.FeaturedImagePath == "" {
		t.Fatal("response missing featured_image_path")
	}

	if !strings.HasSuffix(got.FeaturedImagePath, ".jpg") {
		t.Errorf("featured_image_path: got %q, want .jpg suffix", got.FeaturedImagePath)
	}
	// Original on disk.
	originalFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(got.FeaturedImagePath))
	if _, err := os.Stat(originalFull); err != nil {
		t.Fatalf("original missing on disk: %v", err)
	}
	// One pending job — process it inline.
	runVariantsInline(t, env)
	for _, suffix := range []string{"s", "m", "l"} {
		variantFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(
			strings.TrimSuffix(got.FeaturedImagePath, ".jpg")+"-"+suffix+".jpg",
		))
		if _, err := os.Stat(variantFull); err != nil {
			t.Errorf("variant %s missing: %v", suffix, err)
		}
	}
}

// TestCreatePostWithoutImage — no file part, no path on response, no job
// enqueued. The "default" path for posts that don't need a featured image.
func TestCreatePostWithoutImage(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"no image","markdown_content":"# none of these","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	if _, present := got["featured_image_path"]; present {
		t.Errorf("featured_image_path should be omitted when absent: %v", got["featured_image_path"])
	}
	// Queue is empty.
	if _, err := env.app.jobsWorker.Repo.Claim(context.Background()); !errors.Is(err, jobs.ErrNoJob) {
		t.Errorf("expected no job, got: %v", err)
	}
}

// TestCreatePostRejectsGIF — only jpeg/png accepted; gif → 415.
func TestCreatePostRejectsGIF(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"gif","markdown_content":"# gif rejection test","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "anim.gif", makeGIF(t, 1024, 1024))
	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("gif upload: got %d, want 415; body=%s", rec.Code, rec.Body.String())
	}
}

// TestCreatePostRejectsSmall — image below MinDimension → 400 with field.
func TestCreatePostRejectsSmall(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"small","markdown_content":"# small image test","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "tiny.jpg", makeJPEG(t, 400, 400))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("small jpeg: got %d, want 400; body=%s", rec.Code, rec.Body.String())
	}

	assertValidationFields(t, rec.Body.Bytes(), map[string]string{"featured_image": "image must be at least 800x800 pixels"})
}

// TestUpdatePostReplaceImage — replacing the image deletes old files and
// writes new ones.
func TestUpdatePostReplaceImage(t *testing.T) {
	env := setupPostWriteEnv(t)
	// First create a post WITH an image.
	body := `{"title":"first","markdown_content":"# first markdown content","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "first.jpg", makeJPEG(t, 1024, 1024))
	if rec.Code != http.StatusCreated {
		t.Fatalf("first create: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID                int64  `json:"id"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}

	oldFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(created.FeaturedImagePath))
	runVariantsInline(t, env) // generate the old variants too
	if _, err := os.Stat(oldFull); err != nil {
		t.Fatalf("old original should exist: %v", err)
	}

	// PUT a brand-new image — should replace.
	rec = doMultipartPost(
		t,
		env.app.r,
		http.MethodPut,
		fmt.Sprintf("/admin/posts/%d", created.ID),
		env.tokens["Admin"],
		`{"title":"first","markdown_content":"# first markdown body","category_id":1}`,
		"second.png",
		makePNG(t, 1024, 1024),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var updated struct {
		FeaturedImagePath string `json:"featured_image_path"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode update: %v", err)
	}

	if updated.FeaturedImagePath == created.FeaturedImagePath {
		t.Error("path should change after replace")
	}

	if !strings.HasSuffix(updated.FeaturedImagePath, ".png") {
		t.Errorf("new path: got %q, want .png suffix", updated.FeaturedImagePath)
	}
	// Old original is gone.
	if _, err := os.Stat(oldFull); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("old file should be deleted: stat err=%v", err)
	}
	// New original exists.
	newFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(updated.FeaturedImagePath))
	if _, err := os.Stat(newFull); err != nil {
		t.Errorf("new original missing: %v", err)
	}
}

// TestUpdatePostRemoveImage — remove_featured_image:true wipes the image.
func TestUpdatePostRemoveImage(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"with image","markdown_content":"# body content","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "x.jpg", makeJPEG(t, 1024, 1024))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID                int64  `json:"id"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	runVariantsInline(t, env)
	origFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(created.FeaturedImagePath))

	updateBody := `{"title":"with image","markdown_content":"# body content","category_id":1,"remove_featured_image":true}`
	rec = doMultipartPost(t, env.app.r, http.MethodPut, fmt.Sprintf("/admin/posts/%d", created.ID), env.tokens["Admin"], updateBody, "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("update remove: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var updated map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if v, ok := updated["featured_image_path"]; ok && v != "" {
		t.Errorf("featured_image_path should be cleared, got %v", v)
	}

	if _, err := os.Stat(origFull); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original should be deleted: %v", err)
	}
}

// TestUpdatePostKeepImage — neither file nor remove flag → keep existing.
func TestUpdatePostKeepImage(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"keep me","markdown_content":"# body content","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "k.jpg", makeJPEG(t, 1024, 1024))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID                int64  `json:"id"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// PUT with no file and no remove flag.
	rec = doMultipartPost(
		t,
		env.app.r,
		http.MethodPut,
		fmt.Sprintf("/admin/posts/%d", created.ID),
		env.tokens["Admin"],
		`{"title":"keep me","markdown_content":"# body content","category_id":1}`,
		"",
		nil,
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("update keep: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var updated struct {
		FeaturedImagePath string `json:"featured_image_path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.FeaturedImagePath != created.FeaturedImagePath {
		t.Errorf("path should be preserved: got %q, want %q", updated.FeaturedImagePath, created.FeaturedImagePath)
	}
}

// TestDeletePostCascadesImage — DELETE wipes the original + variants.
func TestDeletePostCascadesImage(t *testing.T) {
	env := setupPostWriteEnv(t)
	body := `{"title":"doomed","markdown_content":"# bye for now","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Admin"], body, "doomed.jpg", makeJPEG(t, 1024, 1024))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, body=%s", rec.Code, rec.Body.String())
	}

	var created struct {
		ID                int64  `json:"id"`
		FeaturedImagePath string `json:"featured_image_path"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	runVariantsInline(t, env)
	origFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(created.FeaturedImagePath))
	if _, err := os.Stat(origFull); err != nil {
		t.Fatalf("original missing pre-delete: %v", err)
	}

	rec = doJSON(t, env.app.r, http.MethodDelete, fmt.Sprintf("/admin/posts/%d", created.ID), env.tokens["Admin"], "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d", rec.Code)
	}

	if _, err := os.Stat(origFull); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("original should be deleted: %v", err)
	}
	// Variants too.
	for _, suffix := range []string{"s", "m", "l"} {
		variantFull := filepath.Join(env.app.uploadsRoot, filepath.FromSlash(
			strings.TrimSuffix(created.FeaturedImagePath, ".jpg")+"-"+suffix+".jpg",
		))
		if _, err := os.Stat(variantFull); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("variant %s should be deleted: %v", suffix, err)
		}
	}
}

// TestMultipartBodyTooLarge — payload past MultipartBodyLimit → 413.
func TestMultipartBodyTooLarge(t *testing.T) {
	env := setupPostWriteEnv(t)
	// 6 MiB of zero bytes — past the 5 MiB cap with slack.
	huge := make([]byte, 6*1024*1024)
	body := `{"title":"big","markdown_content":"# big payload test","category_id":1}`
	rec := doMultipartPost(t, env.app.r, http.MethodPost, "/admin/posts", env.tokens["Author"], body, "huge.jpg", huge)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversize: got %d, want 413; body=%s", rec.Code, rec.Body.String())
	}
	// Sanity check on the constant — guard against accidental loosening.
	if postsh.MultipartBodyLimit > 6*1024*1024 {
		t.Fatalf("MultipartBodyLimit changed past 6 MiB — update this test")
	}
}
