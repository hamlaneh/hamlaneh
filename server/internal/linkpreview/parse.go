package linkpreview

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	// Registered for its decoder only. image.DecodeConfig and image.Decode
	// dispatch on the sniffed format, and a format nobody imported is simply
	// unknown; jpeg and png are imported above for their encoders and
	// register themselves too. WebP is deliberately absent — decoding it
	// needs a dependency, and a preview image that does not decode costs the
	// card its picture and nothing else.
	_ "image/gif"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/net/html"
)

const (
	// maxURLLen matches the url column's cap in migration 0008.
	maxURLLen = 2048
	// The text caps. A preview card is two lines and a heading in every
	// design that has ever existed; anything past this is somebody trying to
	// use a stranger's <meta> tag as a message body.
	maxTitleRunes       = 200
	maxDescriptionRunes = 500

	// maxImagePixels is the before-decode dimension cap of ADR 003: 40
	// megapixels of header-declared image. An image bomb then costs a header
	// parse instead of the gigabytes its pixels would allocate.
	maxImagePixels = 40_000_000
	// maxImageEdge is the long edge of the stored derivative.
	maxImageEdge = 512
	// jpegQuality for the derivative. It is a thumbnail on a card, not an
	// archival copy.
	jpegQuality = 82
)

// urlPattern finds a bare http(s) URL in message text. The message is
// Markdown, but a Markdown link's target is inside the source too, so
// scanning the raw text finds both `https://x` and `[x](https://x)`.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"'` + "`" + `)\]]+`)

// firstURL returns the first http(s) URL in a message's content, or "".
//
// Trailing sentence punctuation is trimmed, because "look at https://go.dev."
// is a sentence and not a request for a page whose path ends in a dot. A URL
// that legitimately ends in one of those characters loses it; the cost of
// that is a preview of a slightly wrong URL on a rare link, which is
// strictly better than the cost of the other choice on every ordinary one.
func firstURL(content string) string {
	raw := strings.TrimRight(urlPattern.FindString(content), ".,;:!?}")
	if raw == "" || len(raw) > maxURLLen {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return ""
	}
	return raw
}

// pageMeta is what one HTML page offers a preview card.
type pageMeta struct {
	ogTitle       string
	htmlTitle     string
	ogDescription string
	metaName      string
	image         string
}

// title prefers OpenGraph over <title>: og:title is what the page's author
// chose to be shared as, and <title> is what a tab has room for.
func (m pageMeta) title() string {
	return clip(firstNonEmpty(m.ogTitle, m.htmlTitle), maxTitleRunes)
}

// description prefers og:description over the plain description meta, for
// the same reason.
func (m pageMeta) description() string {
	return clip(firstNonEmpty(m.ogDescription, m.metaName), maxDescriptionRunes)
}

// parseMeta reads the preview fields out of an HTML document.
//
// A tokenizer rather than the tree parser: everything worth reading is in
// <head>, so this stops at <body> and never allocates a node for the
// document. Malformed markup is not an error — the tokenizer stops, and
// whatever was found before it stopped is what the card gets.
func parseMeta(r io.Reader) pageMeta {
	var meta pageMeta
	tokenizer := html.NewTokenizer(r)

	for {
		switch tokenizer.Next() {
		case html.ErrorToken:
			return meta
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := tokenizer.TagName()
			switch string(name) {
			case "meta":
				readMetaTag(tokenizer, hasAttr, &meta)
			case "title":
				if meta.htmlTitle == "" && tokenizer.Next() == html.TextToken {
					meta.htmlTitle = strings.TrimSpace(string(tokenizer.Text()))
				}
			case "body":
				// <head> is over; nothing past here belongs on a card.
				return meta
			}
		case html.TextToken, html.EndTagToken, html.CommentToken, html.DoctypeToken:
			// Nothing to read.
		}
	}
}

// readMetaTag folds one <meta> element into meta. The first value for a key
// wins: a page that declares og:title twice gets the one it declared first,
// rather than letting a later tag overwrite it.
func readMetaTag(tokenizer *html.Tokenizer, hasAttr bool, meta *pageMeta) {
	var key, content string
	for hasAttr {
		var name, value []byte
		name, value, hasAttr = tokenizer.TagAttr()
		switch string(name) {
		case "property", "name":
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(string(value)))
			}
		case "content":
			content = strings.TrimSpace(string(value))
		}
	}
	if content == "" {
		return
	}

	switch key {
	case "og:title":
		setIfEmpty(&meta.ogTitle, content)
	case "og:description":
		setIfEmpty(&meta.ogDescription, content)
	case "description":
		setIfEmpty(&meta.metaName, content)
	case "og:image", "og:image:url", "og:image:secure_url":
		setIfEmpty(&meta.image, content)
	}
}

func setIfEmpty(field *string, value string) {
	if *field == "" {
		*field = value
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// clip truncates to at most limit runes, counting runes rather than bytes so
// a Persian title is capped at the same length an English one is. It also
// collapses whitespace: a <meta> tag's content is frequently wrapped across
// source lines, and a card is one line of text.
func clip(s string, limit int) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	return string([]rune(s)[:limit])
}

// resolveImageURL turns an og:image into an absolute http(s) URL against the
// page it came from, so a relative "/card.png" still resolves. Anything that
// is not http(s) after resolution — data:, javascript:, a mailto — is
// dropped rather than handed to the fetcher.
func resolveImageURL(pageURL, imageRef string) string {
	if imageRef == "" {
		return ""
	}
	base, err := url.Parse(pageURL)
	if err != nil {
		return ""
	}
	ref, err := url.Parse(imageRef)
	if err != nil {
		return ""
	}
	resolved := base.ResolveReference(ref)
	if resolved.Scheme != "http" && resolved.Scheme != "https" {
		return ""
	}
	if resolved.Host == "" || len(resolved.String()) > maxURLLen {
		return ""
	}
	return resolved.String()
}

// boundedImage turns fetched image bytes into the derivative that gets
// stored: at most maxImageEdge on its long edge, re-encoded, and carrying
// none of the original's metadata.
//
// The dimension cap is checked against the header before any pixels are
// decoded (ADR 003), so an image declaring 30000x30000 costs a header parse
// rather than 3.6 GB of allocation. Re-encoding is also what strips EXIF:
// the encoder writes only the pixels it was given, so a GPS tag cannot
// survive into something a reader downloads.
func boundedImage(data []byte) ([]byte, error) {
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image header: %w", err)
	}
	if int64(config.Width)*int64(config.Height) > maxImagePixels {
		return nil, fmt.Errorf("image is %dx%d, over the %d pixel cap",
			config.Width, config.Height, maxImagePixels)
	}

	decoded, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}

	var out bytes.Buffer
	scaled := scaleToFit(decoded, maxImageEdge)
	// PNG keeps its alpha; everything else — including an animated GIF,
	// which becomes its first frame — is a JPEG.
	if format == "png" {
		if err = png.Encode(&out, scaled); err != nil {
			return nil, fmt.Errorf("encode png derivative: %w", err)
		}
		return out.Bytes(), nil
	}
	if err = jpeg.Encode(&out, scaled, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return nil, fmt.Errorf("encode jpeg derivative: %w", err)
	}
	return out.Bytes(), nil
}

// scaleToFit shrinks src so its long edge is at most edge, preserving aspect
// ratio. An image already inside the bound is returned untouched — it still
// gets re-encoded by the caller, which is what strips its metadata.
func scaleToFit(src image.Image, edge int) image.Image {
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= edge && height <= edge {
		return src
	}

	if width >= height {
		height = max(height*edge/width, 1)
		width = edge
	} else {
		width = max(width*edge/height, 1)
		height = edge
	}

	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.ApproxBiLinear.Scale(dst, dst.Bounds(), src, bounds, xdraw.Src, nil)
	return dst
}
