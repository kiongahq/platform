package editorial

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"net/http"

	"golang.org/x/image/draw"
	"golang.org/x/image/webp"
)

// Upload limits.
const (
	MaxUploadBytes = 10 << 20
	MaxImageSide   = 12000
	MaxImagePixels = 50_000_000
)

// VariantWidths are the responsive widths generated for every image.
var VariantWidths = []int{480, 960, 1600}

var (
	ErrTooLarge         = errors.New("image is larger than 10 MB")
	ErrUnsupportedImage = errors.New("only JPEG, PNG, WebP and GIF images are accepted")
	ErrImageDimensions  = errors.New("image dimensions are too large")
)

// EncodedVariant is one re-encoded size of an upload.
type EncodedVariant struct {
	Name        string
	Width       int
	Height      int
	ContentType string
	Data        []byte
}

// ProcessedImage is the result of ProcessImage.
type ProcessedImage struct {
	SourceType string
	Width      int
	Height     int
	Variants   []EncodedVariant
}

// ProcessImage validates an upload by content sniffing (never by filename or
// client Content-Type), decodes it, applies the EXIF orientation, and
// re-encodes resized variants. Re-encoding from decoded pixels drops all
// metadata (EXIF GPS, camera serials, XMP, comments). Animated GIFs keep only
// their first frame.
func ProcessImage(data []byte) (ProcessedImage, error) {
	var result ProcessedImage
	if len(data) > MaxUploadBytes {
		return result, ErrTooLarge
	}
	sniffed := http.DetectContentType(data)
	var decodeConfig func([]byte) (image.Config, error)
	var decode func([]byte) (image.Image, error)
	switch sniffed {
	case "image/jpeg":
		decodeConfig = func(b []byte) (image.Config, error) { return jpeg.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return jpeg.Decode(bytes.NewReader(b)) }
	case "image/png":
		decodeConfig = func(b []byte) (image.Config, error) { return png.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return png.Decode(bytes.NewReader(b)) }
	case "image/gif":
		decodeConfig = func(b []byte) (image.Config, error) { return gif.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return gif.Decode(bytes.NewReader(b)) }
	case "image/webp":
		decodeConfig = func(b []byte) (image.Config, error) { return webp.DecodeConfig(bytes.NewReader(b)) }
		decode = func(b []byte) (image.Image, error) { return webp.Decode(bytes.NewReader(b)) }
	default:
		return result, ErrUnsupportedImage
	}
	config, err := decodeConfig(data)
	if err != nil {
		return result, ErrUnsupportedImage
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > MaxImageSide || config.Height > MaxImageSide || config.Width*config.Height > MaxImagePixels {
		return result, ErrImageDimensions
	}
	img, err := decode(data)
	if err != nil {
		return result, ErrUnsupportedImage
	}
	if sniffed == "image/jpeg" {
		img = applyOrientation(img, jpegOrientation(data))
	}
	bounds := img.Bounds()
	result.SourceType, result.Width, result.Height = sniffed, bounds.Dx(), bounds.Dy()
	opaque := isOpaque(img)
	// Decide the variant set smallest-first, then scale largest-first so each
	// smaller size is resampled from the previous one: the cost stays near
	// one full-resolution pass even for 12-megapixel photos.
	type plan struct {
		name          string
		width, height int
	}
	var plans []plan
	for _, target := range VariantWidths {
		width := min(target, result.Width)
		height := max(1, int(float64(result.Height)*float64(width)/float64(result.Width)+0.5))
		plans = append(plans, plan{itoa(target), width, height})
		if width >= result.Width {
			break
		}
	}
	variants := make([]EncodedVariant, len(plans))
	source := img
	for index := len(plans) - 1; index >= 0; index-- {
		p := plans[index]
		var scaled draw.Image
		if opaque {
			scaled = image.NewRGBA(image.Rect(0, 0, p.width, p.height))
			draw.Draw(scaled, scaled.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
		} else {
			scaled = image.NewNRGBA(image.Rect(0, 0, p.width, p.height))
		}
		draw.CatmullRom.Scale(scaled, scaled.Bounds(), source, source.Bounds(), draw.Over, nil)
		source = scaled
		var buffer bytes.Buffer
		contentType := "image/jpeg"
		if opaque {
			err = jpeg.Encode(&buffer, scaled, &jpeg.Options{Quality: 85})
		} else {
			contentType = "image/png"
			err = (&png.Encoder{CompressionLevel: png.BestCompression}).Encode(&buffer, scaled)
		}
		if err != nil {
			return result, err
		}
		variants[index] = EncodedVariant{Name: p.name, Width: p.width, Height: p.height, ContentType: contentType, Data: buffer.Bytes()}
	}
	result.Variants = variants
	return result, nil
}

func itoa(value int) string {
	switch value {
	case 480:
		return "480"
	case 960:
		return "960"
	}
	return "1600"
}

func isOpaque(img image.Image) bool {
	if opaque, ok := img.(interface{ Opaque() bool }); ok {
		return opaque.Opaque()
	}
	bounds := img.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if _, _, _, a := img.At(x, y).RGBA(); a != 0xffff {
				return false
			}
		}
	}
	return true
}

// jpegOrientation reads the EXIF orientation tag (1-8) from a JPEG; 1 when
// absent or unreadable.
func jpegOrientation(data []byte) int {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return 1
	}
	for i := 2; i+4 <= len(data); {
		if data[i] != 0xFF {
			return 1
		}
		marker := data[i+1]
		if marker == 0xDA || marker == 0xD9 {
			return 1
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 || i+2+length > len(data) {
			return 1
		}
		segment := data[i+4 : i+2+length]
		if marker == 0xE1 && len(segment) > 14 && string(segment[:6]) == "Exif\x00\x00" {
			return tiffOrientation(segment[6:])
		}
		i += 2 + length
	}
	return 1
}

func tiffOrientation(tiff []byte) int {
	if len(tiff) < 8 {
		return 1
	}
	var order binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		order = binary.LittleEndian
	case "MM":
		order = binary.BigEndian
	default:
		return 1
	}
	offset := int(order.Uint32(tiff[4:8]))
	if offset+2 > len(tiff) {
		return 1
	}
	count := int(order.Uint16(tiff[offset : offset+2]))
	for entry := 0; entry < count; entry++ {
		start := offset + 2 + entry*12
		if start+12 > len(tiff) {
			return 1
		}
		if order.Uint16(tiff[start:start+2]) == 0x0112 {
			value := int(order.Uint16(tiff[start+8 : start+10]))
			if value >= 1 && value <= 8 {
				return value
			}
			return 1
		}
	}
	return 1
}

// applyOrientation rotates/flips so pixels display upright without EXIF.
func applyOrientation(img image.Image, orientation int) image.Image {
	if orientation <= 1 || orientation > 8 {
		return img
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	outW, outH := w, h
	if orientation >= 5 {
		outW, outH = h, w
	}
	out := image.NewNRGBA(image.Rect(0, 0, outW, outH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orientation {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			out.Set(dx, dy, img.At(bounds.Min.X+x, bounds.Min.Y+y))
		}
	}
	return out
}
