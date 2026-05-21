package imagex

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"

	"github.com/disintegration/imaging"
)

const JPEGQuality = 85

// MinDimension is the minimum width AND height we accept on upload.
// 800 is the largest variant's edge, so anything smaller would have to be upscaled for at least one variant.
const MinDimension = 800

var (
	// ErrUnsupportedFormat fires when imaging.Decode reports a format other than JPEG or PNG. The handler maps this to 415.
	ErrUnsupportedFormat = errors.New("imagex: unsupported format (jpeg and png only)")

	// ErrTooSmall fires when either dimension is below MinDimension.
	ErrTooSmall = errors.New("imagex: image is too small")

	// ErrDecode wraps any other decode failure (truncated bytes, malformed header, etc). The handler maps this to 400.
	ErrDecode = errors.New("imagex: could not decode image")
)

var VariantSizes = []struct {
	Suffix string
	Edge   int
}{
	{"s", 200},
	{"m", 360},
	{"l", 800},
}

// ValidateAndDecode parses data, returning the decoded image, the canonical format string ("jpeg" or "png"),
// and the canonical lowercase extension (".jpg" or ".png"). EXIF orientation is applied automatically.
func ValidateAndDecode(r io.ReadSeeker) (image.Image, string, string, error) {
	_, format, err := image.DecodeConfig(r)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrDecode, err)
	}

	var ext string
	switch format {
	case "jpeg":
		ext = ".jpg"
	case "png":
		ext = ".png"
	default:
		return nil, "", "", fmt.Errorf("%w: got %q", ErrUnsupportedFormat, format)
	}

	// Rewind the decode. imaging.Decode auto-detects the format and consumes EXIF orientation when AutoOrientation(true) is set.
	// It also normalizes the underlying image type to *image.NRGBA, which simplifies downstream encoding.
	if _, err := r.Seek(0, io.SeekStart); err != nil {
		return nil, "", "", fmt.Errorf("%w: rewind for decode: %v", ErrDecode, err)
	}

	img, err := imaging.Decode(r, imaging.AutoOrientation(true))
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: %v", ErrDecode, err)
	}

	bounds := img.Bounds()
	if bounds.Dx() < MinDimension || bounds.Dy() < MinDimension {
		return nil, "", "", fmt.Errorf("%w: %dx%d (min %dx%d)", ErrTooSmall, bounds.Dx(), bounds.Dy(), MinDimension, MinDimension)
	}

	return img, format, ext, nil
}

// SquareVariant returns a size×size centered-square crop of img, scaled with Lanczos resampling.
// Source images that are already square are simply downscaled; landscape/portrait sources are center-cropped first.
func SquareVariant(img image.Image, size int) image.Image {
	return imaging.Fill(img, size, size, imaging.Center, imaging.Lanczos)
}

// Encode writes img to w in the given format with web-optimized settings. format must be "jpeg" or "png".
func Encode(img image.Image, format string, w io.Writer) error {
	switch format {
	case "jpeg":
		return jpeg.Encode(w, img, &jpeg.Options{Quality: JPEGQuality})
	case "png":
		pngEnc := png.Encoder{CompressionLevel: png.DefaultCompression}
		return pngEnc.Encode(w, img)
	default:
		return fmt.Errorf("imagex: cannot encode unsupported format %q", format)
	}
}

// EncodeBytes is a convenience wrapper that returns the encoded image as a byte slice.
// The Storage.SaveOriginal API consumes bytes directly so this avoids a temp file or buffer dance at every callsite.
func EncodeBytes(img image.Image, format string) ([]byte, error) {
	var buf bytes.Buffer
	if err := Encode(img, format, &buf); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}
