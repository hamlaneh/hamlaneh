package httpserver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"

	_ "image/gif" // registers the GIF decoder; see the note below

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp" // registers the WebP decoder; see the note below
)

// The two blank imports above are the GIF and WebP decoders. image.Decode
// and image.DecodeConfig dispatch on the registered format list, and an
// unregistered format is simply not readable — which for this package would
// mean a WebP silently failing its own sniff. jpeg and png are imported
// normally because thumbnails are encoded with them.

// The ingest limits of ADR 003.
const (
	// maxImagePixels bounds an image by its header-declared dimensions,
	// checked before any decode: a decompression bomb then costs a header
	// parse rather than a gigabyte of pixels.
	maxImagePixels = 40_000_000
	// thumbMaxEdge is the thumbnail's long edge.
	thumbMaxEdge = 512
	// thumbJPEGQuality is the quality thumbnails derived from a JPEG are
	// re-encoded at. A preview is not the file; the original is downloadable
	// and untouched.
	thumbJPEGQuality = 80
)

// ingestibleImageTypes are the only media types decoded, stripped and
// thumbnailed here — the same four the files origin will ever serve inline
// (files_origin.go: inlineImageTypes, which the orchestrator should collapse
// into one list). Everything else — SVG, HTML, PDF, anything — is an opaque
// blob (ADR 003).
var ingestibleImageTypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/webp": true,
	"image/gif":  true,
}

// The two ways an upload declaring an image can be refused.
var (
	// errContentTypeMismatch is the 415: the label said image/*, the bytes
	// disagreed. It covers both directions of the mismatch — a PNG labelled
	// image/jpeg, and an SVG or a zip labelled as any image at all.
	errContentTypeMismatch = errors.New("declared image type does not match the bytes")
	// errImageTooLarge is the header-declared pixel count over the limit.
	errImageTooLarge = errors.New("image dimensions exceed the limit")
)

// ingestedImage is one image after ADR 003's ingest: the stored original
// with its metadata segments removed, the dimensions read from its header,
// and the thumbnail derived from the stripped bytes.
type ingestedImage struct {
	// original is what gets stored — the same pixels, without the segments
	// that carried EXIF, XMP or comments.
	original []byte
	width    int
	height   int
	// thumbnail is nil when the image is already inside thumbMaxEdge, in
	// which case the card previews the original and has_thumbnail is false.
	thumbnail []byte
}

// sniffImageType reports which of the four inline image types head's bytes
// are, or "" for anything else.
//
// net/http's sniffer is the whole implementation on purpose: it already
// knows these four signatures (WebP's RIFF container included), it is the
// same table browsers agree with closely enough to matter, and a
// hand-written magic-number table here would be one more thing to get wrong.
func sniffImageType(head []byte) string {
	sniffed := http.DetectContentType(head)
	if ingestibleImageTypes[sniffed] {
		return sniffed
	}
	return ""
}

// ingestImage validates, strips and thumbnails an upload declared as an
// image. declared is the media type the client labelled the part with,
// already parsed down to its type/subtype.
//
// The label decides nothing but what must be proved: the bytes have to sniff
// as exactly the type declared, or the upload is refused (415). That is what
// keeps "only images are served inline" true — a file that reaches the
// inline path has had its bytes read as that image type, not merely its name.
func ingestImage(declared string, data []byte) (ingestedImage, error) {
	if sniffImageType(data) != declared {
		return ingestedImage{}, errContentTypeMismatch
	}

	// Dimensions first, and from the header alone: DecodeConfig parses the
	// few bytes that declare the size and allocates nothing pixel-shaped, so
	// a 40-megapixel claim in a 20-kilobyte file is refused before anything
	// tries to make it real.
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return ingestedImage{}, errContentTypeMismatch
	}
	if cfg.Width*cfg.Height > maxImagePixels {
		return ingestedImage{}, errImageTooLarge
	}

	stripped := stripImageMetadata(declared, data)
	// Stripping is a segment edit, not a re-encode, so it must leave an image
	// that still decodes as the same one. If it somehow did not, the honest
	// answer is to store nothing rather than something unreadable.
	if after, _, cfgErr := image.DecodeConfig(bytes.NewReader(stripped)); cfgErr != nil ||
		after.Width != cfg.Width || after.Height != cfg.Height {
		return ingestedImage{}, fmt.Errorf("stripping %s metadata damaged the image", declared)
	}

	img := ingestedImage{original: stripped, width: cfg.Width, height: cfg.Height}
	if max(cfg.Width, cfg.Height) <= thumbMaxEdge {
		// Already preview-sized; a derivative would be a second copy of the
		// same picture. The card previews the original.
		return img, nil
	}
	if img.thumbnail, err = thumbnail(stripped, format); err != nil {
		return ingestedImage{}, err
	}
	return img, nil
}

// thumbnail decodes the stripped original and renders it down to
// thumbMaxEdge on its long edge.
//
// An animated GIF thumbnails as its first frame, which is what image.Decode
// returns and what a still preview should be anyway.
func thumbnail(data []byte, format string) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image for thumbnail: %w", err)
	}

	b := src.Bounds()
	scale := float64(thumbMaxEdge) / float64(max(b.Dx(), b.Dy()))
	dst := image.NewRGBA(image.Rect(0, 0, max(1, int(float64(b.Dx())*scale)), max(1, int(float64(b.Dy())*scale))))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)

	var out bytes.Buffer
	// A photograph's preview stays a photograph; everything else keeps its
	// alpha channel, which JPEG cannot carry.
	if format == "jpeg" {
		err = jpeg.Encode(&out, dst, &jpeg.Options{Quality: thumbJPEGQuality})
	} else {
		err = png.Encode(&out, dst)
	}
	if err != nil {
		return nil, fmt.Errorf("encode thumbnail: %w", err)
	}
	return out.Bytes(), nil
}

// stripImageMetadata removes the metadata segments of an image without
// touching its pixels (ADR 003: removal, not re-encoding — a re-encode would
// quietly degrade every photo the instance stores).
//
// Anything it cannot parse it returns unchanged, and the caller re-reads the
// result before storing it: a stripper that mangles an image is worse than
// one that occasionally leaves a comment behind, and the formats here are
// full of files that are technically malformed and still render everywhere.
func stripImageMetadata(mediaType string, data []byte) []byte {
	switch mediaType {
	case "image/jpeg":
		return stripJPEG(data)
	case "image/png":
		return stripPNG(data)
	case "image/webp":
		return stripWebP(data)
	default:
		// GIF carries no EXIF: its only metadata is the comment and
		// application extensions, which hold no capture data. Nothing to do.
		return data
	}
}

// stripJPEG drops every APPn segment (APP1 is EXIF, APP2 is ICC, APP13 is
// IPTC, and the rest are as varied) and every comment, keeping the frame,
// the tables and the scan.
//
// Everything from the first scan header onwards is copied verbatim: past
// that point the file is entropy-coded data with no segment structure to
// walk, and guessing where the next marker starts inside it is how strippers
// corrupt images.
func stripJPEG(data []byte) []byte {
	const (
		markerPrefix = 0xFF
		markerSOI    = 0xD8
		markerSOS    = 0xDA
		markerCOM    = 0xFE
		markerAPP0   = 0xE0
		markerAPP15  = 0xEF
	)
	if len(data) < 2 || data[0] != markerPrefix || data[1] != markerSOI {
		return data
	}

	out := make([]byte, 0, len(data))
	out = append(out, data[0], data[1])

	for i := 2; ; {
		// A segment is FF, the marker, then a two-byte length that counts
		// itself. Anything else means the structure ended before the scan
		// did; hand back what came in rather than guessing.
		if i+4 > len(data) || data[i] != markerPrefix {
			return data
		}
		marker := data[i+1]
		if marker == markerSOS {
			return append(out, data[i:]...)
		}
		length := int(binary.BigEndian.Uint16(data[i+2 : i+4]))
		if length < 2 || i+2+length > len(data) {
			return data
		}
		isAPPn := marker >= markerAPP0 && marker <= markerAPP15
		if !isAPPn && marker != markerCOM {
			out = append(out, data[i:i+2+length]...)
		}
		i += 2 + length
	}
}

// pngKeptAncillary are the ancillary chunks that survive stripping: the ones
// that decide how the pixels look rather than where they came from.
//
// tRNS is the reason this is an allowlist and not "drop every lowercase
// chunk": it carries a palette image's transparency, and dropping it turns
// transparent pixels opaque. The colour chunks are kept for the same reason —
// they change rendering. iCCP is deliberately not here, matching the JPEG
// side's removal of APP2; embedded profiles are metadata a chat does not owe
// its readers, and the sRGB fallback is what browsers assume anyway.
var pngKeptAncillary = map[string]bool{
	"tRNS": true,
	"gAMA": true,
	"cHRM": true,
	"sRGB": true,
}

// stripPNG keeps the critical chunks and the rendering-relevant ancillary
// ones, dropping the rest — tEXt, zTXt, iTXt, eXIf, tIME and anything else a
// camera or an editor left behind.
func stripPNG(data []byte) []byte {
	signature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1A, '\n'}
	if !bytes.HasPrefix(data, signature) {
		return data
	}

	out := make([]byte, 0, len(data))
	out = append(out, signature...)

	for i := len(signature); ; {
		// length (4) + type (4) + data + CRC (4).
		if i+8 > len(data) {
			return data
		}
		length := int(binary.BigEndian.Uint32(data[i : i+4]))
		if length < 0 || i+12+length > len(data) {
			return data
		}
		chunkType := string(data[i+4 : i+8])
		chunk := data[i : i+12+length]

		// A chunk is critical when its type's first letter is upper case.
		critical := chunkType[0] >= 'A' && chunkType[0] <= 'Z'
		if critical || pngKeptAncillary[chunkType] {
			out = append(out, chunk...)
		}
		i += 12 + length
		if chunkType == "IEND" {
			// Whatever trails IEND is not part of the image. Dropping it is
			// the point: it is where a smuggled payload would sit.
			return out
		}
	}
}

// webpDroppedChunks are the RIFF chunks stripped from a WebP: the metadata
// containers, plus the colour profile, matching what the JPEG and PNG paths
// drop.
var webpDroppedChunks = map[string]bool{
	"EXIF": true,
	"XMP ": true,
	"ICCP": true,
}

// stripWebP walks the RIFF container, drops the metadata chunks, clears the
// flags in VP8X that advertised them, and rewrites the file size.
//
// A WebP can carry EXIF exactly as a JPEG can — the extended format has a
// chunk for it — so an unstripped WebP is the same GPS leak in a different
// wrapper.
func stripWebP(data []byte) []byte {
	const (
		headerLen  = 12 // "RIFF" + size + "WEBP"
		chunkHdr   = 8  // FourCC + size
		vp8xFlags  = 0  // offset of the flag byte inside a VP8X payload
		flagICC    = 0x20
		flagEXIF   = 0x08
		flagXMP    = 0x04
		flagsToClr = flagICC | flagEXIF | flagXMP
	)
	if len(data) < headerLen || !bytes.HasPrefix(data, []byte("RIFF")) ||
		!bytes.Equal(data[8:12], []byte("WEBP")) {
		return data
	}

	out := make([]byte, headerLen, len(data))
	copy(out, data[:headerLen])

	for i := headerLen; i < len(data); {
		if i+chunkHdr > len(data) {
			return data
		}
		fourCC := string(data[i : i+4])
		size := int(binary.LittleEndian.Uint32(data[i+4 : i+8]))
		// RIFF chunks are padded to an even length; the pad byte is not
		// counted by the size field.
		padded := size + size%2
		if size < 0 || i+chunkHdr+padded > len(data) {
			return data
		}
		if !webpDroppedChunks[fourCC] {
			start := len(out)
			out = append(out, data[i:i+chunkHdr+padded]...)
			if fourCC == "VP8X" && size > vp8xFlags {
				out[start+chunkHdr+vp8xFlags] &^= flagsToClr
			}
		}
		i += chunkHdr + padded
	}

	// The RIFF size field counts everything after itself.
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8)) // #nosec G115 -- bounded by the upload cap, far below 2^32
	return out
}
