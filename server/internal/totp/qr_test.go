package totp_test

import (
	"encoding/xml"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hamlaneh/hamlaneh/server/internal/totp"
)

var viewBoxPattern = regexp.MustCompile(`viewBox="0 0 (\d+) (\d+)"`)

// TestQRSVGIsWellFormedAndInlineSafe pins the properties the settings screen
// depends on: the answer is a single self-contained SVG document with no
// external reference and no caller-controlled text, so it can be inlined
// under the instance's strict CSP.
func TestQRSVGIsWellFormedAndInlineSafe(t *testing.T) {
	t.Parallel()

	enrollment, err := totp.Enroll(totp.NewSecret(), "a.jones")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	svg := enrollment.QRSVG

	if err := xml.Unmarshal([]byte(svg), new(struct {
		XMLName xml.Name
	})); err != nil {
		t.Fatalf("QR SVG is not well-formed XML: %v", err)
	}
	if !strings.HasPrefix(svg, "<svg ") || !strings.HasSuffix(svg, "</svg>") {
		t.Error("QR SVG is not a single svg element")
	}

	for _, forbidden := range []string{"<script", "<image", "<foreignObject", "href", "url(", "<text"} {
		if strings.Contains(svg, forbidden) {
			t.Errorf("QR SVG contains %q", forbidden)
		}
	}
	// The secret must be in the picture only as modules, never as markup.
	if strings.Contains(svg, enrollment.ManualKey) || strings.Contains(svg, "otpauth") {
		t.Error("QR SVG leaks the enrolment text as markup")
	}
	if !strings.Contains(svg, `aria-hidden="true"`) {
		t.Error("QR SVG is not hidden from assistive technology; the manual key is the readable path")
	}
}

// TestQRSVGGeometry pins the module grid: a real QR version dimension plus
// the four-module quiet zone the specification requires on every side.
func TestQRSVGGeometry(t *testing.T) {
	t.Parallel()

	enrollment, err := totp.Enroll(totp.NewSecret(), "a.jones")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	match := viewBoxPattern.FindStringSubmatch(enrollment.QRSVG)
	if match == nil {
		t.Fatal("QR SVG has no viewBox")
	}
	if match[1] != match[2] {
		t.Errorf("viewBox is not square: %s x %s", match[1], match[2])
	}

	side, err := strconv.Atoi(match[1])
	if err != nil {
		t.Fatalf("viewBox side is not a number: %v", err)
	}
	modules := side - 2*4
	if modules < 21 || (modules-21)%4 != 0 {
		t.Errorf("module grid is %d, which is not a QR version dimension", modules)
	}

	if !strings.Contains(enrollment.QRSVG, `<path fill="#000000" d="M`) {
		t.Error("QR SVG draws no dark modules")
	}
}

// TestQRSVGVariesWithTheSecret guards against a placeholder or cached image
// slipping in: two secrets must not draw the same picture.
func TestQRSVGVariesWithTheSecret(t *testing.T) {
	t.Parallel()

	first, err := totp.Enroll(totp.NewSecret(), "a.jones")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	second, err := totp.Enroll(totp.NewSecret(), "a.jones")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if first.QRSVG == second.QRSVG {
		t.Error("two different secrets rendered an identical QR")
	}
}
