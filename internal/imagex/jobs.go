package imagex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/vpramatarov/micro-blog/internal/uploads"
)

// VariantsPayload is the JSON format of the "image_variants" job.
type VariantsPayload struct {
	Path   string `json:"path"`   // relative path of the original
	Format string `json:"format"` // "jpeg" or "png"
}

// NewVariantsHandler returns a job-queue handler (signature func(ctx, payload) error so it satisfies jobs.Handler without an import dependency)
// that processes "image_variants" jobs. Errors returned trigger the worker's retry-with-backoff up to its configured attempt cap.
func NewVariantsHandler(store *uploads.Storage, log *slog.Logger) func(ctx context.Context, payload []byte) error {
	if log == nil {
		log = slog.Default()
	}

	return func(ctx context.Context, payload []byte) error {
		var p VariantsPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			// Malformed payload — retrying won't help; let the worker mark it failed by returning a sentinel-shaped error.
			return fmt.Errorf("imagex: invalid variants payload: %w", err)
		}

		if p.Format != "jpeg" && p.Format != "png" {
			return fmt.Errorf("imagex: unsupported format %q in payload", p.Format)
		}

		data, err := store.ReadOriginal(p.Path)
		if err != nil {
			return fmt.Errorf("imagex: read original: %w", err)
		}

		img, _, _, err := ValidateAndDecode(bytes.NewReader(data))
		if err != nil {
			return fmt.Errorf("imagex: decode original: %w", err)
		}

		for _, v := range VariantSizes {
			variant := SquareVariant(img, v.Edge)
			encoded, encErr := EncodeBytes(variant, p.Format)
			if encErr != nil {
				return fmt.Errorf("imagex: encode %s variant: %w", v.Suffix, encErr)
			}

			if writeErr := store.SaveVariant(p.Path, v.Suffix, encoded); writeErr != nil {
				return fmt.Errorf("imagex: save %s variant: %w", v.Suffix, writeErr)
			}
		}

		log.Info("image variants generated", "path", p.Path, "format", p.Format)
		return nil
	}
}
