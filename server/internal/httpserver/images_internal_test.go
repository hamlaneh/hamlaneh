package httpserver

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"
)

// The image ingest tests. They are internal because what has to be pinned is
// the shape of the bytes that get stored — that a photograph's GPS
// coordinates are gone from the file the instance keeps, not merely absent
// from some response field.

// solidImage returns a w×h image with enough variation that a JPEG of it is
// not degenerate.
func solidImage(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x % 251), G: uint8(y % 241), B: 0x40, A: 0xff})
		}
	}
	return img
}

// gpsRationals are the three RATIONAL values of a real GPSLatitude tag —
// 51/1, 30/1, 1234/100. They are the bytes every stripping assertion below
// hunts for: if these survive, so does the coordinate.
func gpsRationals() []byte {
	out := make([]byte, 0, 24)
	for _, r := range [][2]uint32{{51, 1}, {30, 1}, {1234, 100}} {
		b := make([]byte, 8)
		binary.LittleEndian.PutUint32(b[0:], r[0])
		binary.LittleEndian.PutUint32(b[4:], r[1])
		out = append(out, b...)
	}
	return out
}

// exifEntry builds one 12-byte TIFF IFD entry.
func exifEntry(tag, typ uint16, count, value uint32) []byte {
	e := make([]byte, 12)
	binary.LittleEndian.PutUint16(e[0:], tag)
	binary.LittleEndian.PutUint16(e[2:], typ)
	binary.LittleEndian.PutUint32(e[4:], count)
	binary.LittleEndian.PutUint32(e[8:], value)
	return e
}

// exifGPSPayload is a real little-endian EXIF block: a TIFF header, an IFD0
// whose single entry is the GPSInfo pointer, and a GPS IFD carrying
// GPSLatitudeRef and GPSLatitude. This is what a phone puts in a photo.
func exifGPSPayload() []byte {
	// IFD0 sits right after the 8-byte TIFF header and is 2 + 12 + 4 bytes,
	// so the GPS IFD starts at 26; the GPS IFD is 2 + 24 + 4, so its
	// out-of-line rationals start at 56.
	const (
		gpsIFDOffset    = 26
		rationalsOffset = 56
	)

	tiff := []byte{'I', 'I', 0x2A, 0x00, 0x08, 0x00, 0x00, 0x00}

	ifd0 := []byte{0x01, 0x00}
	ifd0 = append(ifd0, exifEntry(0x8825, 4, 1, gpsIFDOffset)...) // GPSInfo
	ifd0 = append(ifd0, 0, 0, 0, 0)                               // no next IFD

	gps := []byte{0x02, 0x00}
	gps = append(gps, exifEntry(0x0001, 2, 2, uint32('N'))...)     // GPSLatitudeRef "N"
	gps = append(gps, exifEntry(0x0002, 5, 3, rationalsOffset)...) // GPSLatitude
	gps = append(gps, 0, 0, 0, 0)

	block := append([]byte("Exif\x00\x00"), tiff...)
	block = append(block, ifd0...)
	block = append(block, gps...)
	return append(block, gpsRationals()...)
}

// jpegWithGPS encodes a JPEG and splices a real EXIF GPS segment in after
// the SOI, exactly where a camera puts one.
func jpegWithGPS(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, solidImage(w, h), nil); err != nil {
		t.Fatalf("encode fixture JPEG: %v", err)
	}
	encoded := buf.Bytes()

	payload := exifGPSPayload()
	segment := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(segment[2:], uint16(len(payload)+2))
	segment = append(segment, payload...)

	// SOI, then the APP1, then the rest of the file.
	out := append([]byte{}, encoded[:2]...)
	out = append(out, segment...)
	return append(out, encoded[2:]...)
}

// pngChunk builds one PNG chunk with a correct CRC.
func pngChunk(chunkType string, data []byte) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, uint32(len(data)))
	body := append([]byte(chunkType), data...)
	out = append(out, body...)
	crc := make([]byte, 4)
	binary.BigEndian.PutUint32(crc, crc32.ChecksumIEEE(body))
	return append(out, crc...)
}

// pngWithMetadata encodes a PNG and splices metadata chunks in after IHDR:
// an eXIf carrying the same GPS block, a tEXt comment, and a tRNS that must
// survive because it decides how the pixels render.
func pngWithMetadata(t *testing.T, w, h int) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := png.Encode(&buf, solidImage(w, h)); err != nil {
		t.Fatalf("encode fixture PNG: %v", err)
	}
	encoded := buf.Bytes()

	// The signature is 8 bytes and IHDR is always the first chunk: 8 + 13
	// bytes of data + 12 of framing.
	const afterIHDR = 8 + 13 + 12
	inserted := pngChunk("eXIf", exifGPSPayload())
	inserted = append(inserted, pngChunk("tEXt", []byte("Comment\x00taken at home"))...)
	inserted = append(inserted, pngChunk("tRNS", []byte{0x00, 0x10, 0x00, 0x20, 0x00, 0x30})...)

	out := append([]byte{}, encoded[:afterIHDR]...)
	out = append(out, inserted...)
	return append(out, encoded[afterIHDR:]...)
}

func TestSniffImageTypeTrustsBytesNotNames(t *testing.T) {
	t.Parallel()

	var jpg, pngBuf, gifBuf bytes.Buffer
	if err := jpeg.Encode(&jpg, solidImage(4, 4), nil); err != nil {
		t.Fatalf("encode JPEG: %v", err)
	}
	if err := png.Encode(&pngBuf, solidImage(4, 4)); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}
	if err := gif.Encode(&gifBuf, solidImage(4, 4), nil); err != nil {
		t.Fatalf("encode GIF: %v", err)
	}

	tests := map[string]struct {
		data []byte
		want string
	}{
		"jpeg":          {jpg.Bytes(), "image/jpeg"},
		"png":           {pngBuf.Bytes(), "image/png"},
		"gif":           {gifBuf.Bytes(), "image/gif"},
		"webp":          {syntheticWebP(nil), "image/webp"},
		"svg":           {[]byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`), ""},
		"html":          {[]byte("<!DOCTYPE html><html><body>hi</body></html>"), ""},
		"pdf":           {[]byte("%PDF-1.7\n1 0 obj\n"), ""},
		"empty":         {nil, ""},
		"png then junk": {append([]byte("\x89PNG\r\n\x1a\n"), 0x00), "image/png"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := sniffImageType(tc.data); got != tc.want {
				t.Errorf("sniffImageType = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestIngestImageStripsJPEGGPSFromOriginalAndThumbnail(t *testing.T) {
	t.Parallel()

	// Bigger than the thumbnail edge, so this also exercises the derivative.
	withGPS := jpegWithGPS(t, 900, 600)
	if !bytes.Contains(withGPS, []byte("Exif\x00\x00")) || !bytes.Contains(withGPS, gpsRationals()) {
		t.Fatal("the fixture does not carry EXIF GPS; the rest of this test proves nothing")
	}

	img, err := ingestImage("image/jpeg", withGPS)
	if err != nil {
		t.Fatalf("ingestImage: %v", err)
	}

	if bytes.Contains(img.original, []byte("Exif\x00\x00")) {
		t.Error("the stored original still carries an EXIF segment")
	}
	if bytes.Contains(img.original, gpsRationals()) {
		t.Error("the stored original still carries the GPS coordinates")
	}
	if img.thumbnail == nil {
		t.Fatal("a 900×600 image produced no thumbnail")
	}
	if bytes.Contains(img.thumbnail, gpsRationals()) {
		t.Error("the thumbnail carries the GPS coordinates")
	}

	// Stripping is a segment edit, not a re-encode: same picture, same size.
	if img.width != 900 || img.height != 600 {
		t.Errorf("recorded dimensions %dx%d, want 900x600", img.width, img.height)
	}
	decoded, err := jpeg.Decode(bytes.NewReader(img.original))
	if err != nil {
		t.Fatalf("the stripped original no longer decodes: %v", err)
	}
	if b := decoded.Bounds(); b.Dx() != 900 || b.Dy() != 600 {
		t.Errorf("the stripped original decodes as %v", b)
	}

	// The thumbnail is bounded on its long edge.
	thumb, err := jpeg.Decode(bytes.NewReader(img.thumbnail))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if b := thumb.Bounds(); b.Dx() != thumbMaxEdge || b.Dy() != 341 {
		t.Errorf("thumbnail is %v, want %d on the long edge with the aspect kept", b, thumbMaxEdge)
	}
}

func TestIngestImageStripsPNGMetadataButKeepsRendering(t *testing.T) {
	t.Parallel()

	withMetadata := pngWithMetadata(t, 40, 30)
	img, err := ingestImage("image/png", withMetadata)
	if err != nil {
		t.Fatalf("ingestImage: %v", err)
	}

	if bytes.Contains(img.original, []byte("eXIf")) || bytes.Contains(img.original, gpsRationals()) {
		t.Error("the stored PNG still carries its eXIf chunk")
	}
	if bytes.Contains(img.original, []byte("taken at home")) {
		t.Error("the stored PNG still carries its tEXt comment")
	}
	// tRNS is ancillary but decides how the pixels render; dropping it would
	// turn transparent pixels opaque.
	if !bytes.Contains(img.original, []byte("tRNS")) {
		t.Error("stripping dropped tRNS, which changes what the image looks like")
	}
	if img.thumbnail != nil {
		t.Error("a 40×30 image is already preview-sized and needs no thumbnail")
	}
	if img.width != 40 || img.height != 30 {
		t.Errorf("recorded dimensions %dx%d, want 40x30", img.width, img.height)
	}
}

func TestIngestImageRefusesBytesThatAreNotTheDeclaredType(t *testing.T) {
	t.Parallel()

	var pngBuf bytes.Buffer
	if err := png.Encode(&pngBuf, solidImage(8, 8)); err != nil {
		t.Fatalf("encode PNG: %v", err)
	}

	tests := map[string]struct {
		declared string
		data     []byte
	}{
		"png labelled jpeg": {"image/jpeg", pngBuf.Bytes()},
		"svg labelled svg":  {"image/svg+xml", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		"svg labelled png":  {"image/png", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`)},
		"html labelled gif": {"image/gif", []byte("<!DOCTYPE html><html></html>")},
		"nothing at all":    {"image/png", nil},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ingestImage(tc.declared, tc.data); !errors.Is(err, errContentTypeMismatch) {
				t.Errorf("ingestImage = %v, want errContentTypeMismatch", err)
			}
		})
	}
}

func TestIngestImageRefusesADimensionBombBeforeDecoding(t *testing.T) {
	t.Parallel()

	// A PNG whose IHDR claims 20000×20000 — 400 megapixels — in a file of a
	// few dozen bytes. The refusal must come from the header: decoding this
	// would ask for well over a gigabyte.
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], 20000)
	binary.BigEndian.PutUint32(ihdr[4:], 20000)
	ihdr[8], ihdr[9] = 8, 2 // 8-bit truecolor

	bomb := append([]byte("\x89PNG\r\n\x1a\n"), pngChunk("IHDR", ihdr)...)
	bomb = append(bomb, pngChunk("IDAT", []byte{0x78, 0x9c, 0x03, 0x00, 0x00, 0x00, 0x00, 0x01})...)
	bomb = append(bomb, pngChunk("IEND", nil)...)

	if _, err := ingestImage("image/png", bomb); !errors.Is(err, errImageTooLarge) {
		t.Errorf("ingestImage of a 400-megapixel header = %v, want errImageTooLarge", err)
	}
}

// syntheticWebP builds a RIFF/WEBP container: a VP8X header advertising
// EXIF, a "VP8 " payload, and an EXIF chunk. It is not decodable — x/image
// decodes WebP but nothing in the standard library encodes it — which is
// exactly why stripWebP is tested directly rather than through ingestImage.
func syntheticWebP(extra []byte) []byte {
	chunk := func(fourCC string, payload []byte) []byte {
		out := append([]byte(fourCC), 0, 0, 0, 0)
		binary.LittleEndian.PutUint32(out[4:8], uint32(len(payload)))
		out = append(out, payload...)
		if len(payload)%2 == 1 {
			out = append(out, 0)
		}
		return out
	}

	vp8x := make([]byte, 10)
	vp8x[0] = 0x28 // ICC + EXIF advertised
	body := chunk("VP8X", vp8x)
	body = append(body, chunk("VP8 ", []byte{0x01, 0x02, 0x03, 0x04})...)
	body = append(body, chunk("EXIF", exifGPSPayload())...)
	body = append(body, extra...)

	out := append([]byte("RIFF"), 0, 0, 0, 0)
	out = append(out, []byte("WEBP")...)
	out = append(out, body...)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(out)-8))
	return out
}

func TestStripWebPRemovesMetadataChunks(t *testing.T) {
	t.Parallel()

	withEXIF := syntheticWebP(nil)
	stripped := stripWebP(withEXIF)

	if bytes.Contains(stripped, gpsRationals()) {
		t.Error("the stripped WebP still carries its GPS coordinates")
	}
	if bytes.Contains(stripped, []byte("EXIF")) {
		t.Error("the EXIF chunk survived")
	}
	// The picture itself has to still be there.
	if !bytes.Contains(stripped, []byte("VP8 ")) {
		t.Fatal("stripping removed the image data")
	}
	// The RIFF size field counts everything after itself, and a stale one is
	// how a decoder ends up reading past the file.
	if got := binary.LittleEndian.Uint32(stripped[4:8]); got != uint32(len(stripped)-8) {
		t.Errorf("RIFF size is %d, want %d", got, len(stripped)-8)
	}
	// VP8X must stop advertising the chunks that are gone.
	vp8x := bytes.Index(stripped, []byte("VP8X"))
	if vp8x < 0 {
		t.Fatal("VP8X was dropped")
	}
	if flags := stripped[vp8x+8]; flags&0x2C != 0 {
		t.Errorf("VP8X flags are %#02x, want the ICC, EXIF and XMP bits cleared", flags)
	}
}

func TestStripImageMetadataLeavesUnparseableBytesAlone(t *testing.T) {
	t.Parallel()

	// A stripper that mangles a file is worse than one that leaves a comment
	// behind: these formats are full of technically-malformed files that
	// render everywhere, and ingestImage re-reads the result anyway.
	tests := map[string]struct {
		mediaType string
		data      []byte
	}{
		"truncated jpeg": {"image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE1, 0x00}},
		"truncated png":  {"image/png", []byte("\x89PNG\r\n\x1a\n\x00\x00")},
		"truncated webp": {"image/webp", []byte("RIFF\x04\x00\x00\x00WEB")},
		"not an image":   {"image/png", []byte("hello")},
		"gif untouched":  {"image/gif", []byte("GIF89a and a comment")},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := stripImageMetadata(tc.mediaType, tc.data); !bytes.Equal(got, tc.data) {
				t.Errorf("stripImageMetadata rewrote unparseable bytes:\n got %x\nwant %x", got, tc.data)
			}
		})
	}
}

// FuzzStripImageMetadata is the input-handler fuzz CLAUDE.md requires. The
// stripper walks attacker-controlled segment lengths and chunk sizes, which
// is precisely the shape of code that reads past the end of a slice.
//
// Two invariants beyond "does not panic": stripping never grows a file, and
// it never changes what the bytes sniff as. The second is the one that
// matters — a strip that turned a PNG into something else would have the
// serving side offering bytes nobody ever validated.
func FuzzStripImageMetadata(f *testing.F) {
	var pngBuf, jpgBuf, gifBuf bytes.Buffer
	if err := png.Encode(&pngBuf, solidImage(6, 6)); err != nil {
		f.Fatalf("encode seed PNG: %v", err)
	}
	if err := jpeg.Encode(&jpgBuf, solidImage(6, 6), nil); err != nil {
		f.Fatalf("encode seed JPEG: %v", err)
	}
	if err := gif.Encode(&gifBuf, solidImage(6, 6), nil); err != nil {
		f.Fatalf("encode seed GIF: %v", err)
	}

	seeds := [][]byte{
		pngBuf.Bytes(), jpgBuf.Bytes(), gifBuf.Bytes(),
		syntheticWebP(nil),
		syntheticWebP([]byte("XMP \xff\xff\xff\xff")),
		append([]byte("\x89PNG\r\n\x1a\n"), 0xFF, 0xFF, 0xFF, 0xFF),
		{0xFF, 0xD8, 0xFF, 0xE1, 0xFF, 0xFF},
		[]byte("RIFF\xff\xff\xff\xffWEBPVP8X"),
		nil,
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif"} {
		f.Add([]byte(mediaType))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		before := sniffImageType(data)
		for _, mediaType := range []string{"image/jpeg", "image/png", "image/webp", "image/gif", "application/octet-stream"} {
			stripped := stripImageMetadata(mediaType, data)
			if len(stripped) > len(data) {
				t.Fatalf("stripping %s grew %d bytes into %d", mediaType, len(data), len(stripped))
			}
			if after := sniffImageType(stripped); after != before {
				t.Fatalf("stripping %s changed the sniffed type from %q to %q", mediaType, before, after)
			}
		}
	})
}
