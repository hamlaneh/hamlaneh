package totp

import (
	"fmt"
	"image"
	"image/color"
	"strconv"

	"github.com/boombuler/barcode/qr"
)

// QR geometry.
const (
	// quietZone is the four-module margin the QR specification requires
	// around the symbol; without it scanners lose the finder patterns
	// against a busy background.
	quietZone = 4
	// errorCorrection is the 15% level authenticator QRs conventionally use:
	// enough to survive a phone camera at an angle without inflating the
	// symbol.
	errorCorrection = qr.M
	// darkThreshold splits a module's grayscale value into dark and light.
	darkThreshold = 0x80
)

// svgTemplate is the whole rendered document. Only the module geometry
// varies, so no caller-controlled text ever reaches the markup and the
// result is safe to inline in a page.
//
// The colours are fixed rather than themed: a QR has to be dark-on-light to
// scan, in either theme. It carries no accessible name because the manual
// key beside it is the same secret in readable form — the picture is the
// convenience path, not the only one.
const svgTemplate = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %[1]d %[1]d" ` +
	`width="100%%" height="100%%" shape-rendering="crispEdges" aria-hidden="true" focusable="false">` +
	`<rect width="%[1]d" height="%[1]d" fill="#ffffff"/>` +
	`<path fill="#000000" d="%[2]s"/></svg>`

// renderQR encodes content as a QR symbol and returns it as an inline SVG
// document sized to its container. It is rendered per request from the
// secret and never stored as an image.
func renderQR(content string) (string, error) {
	code, err := qr.Encode(content, errorCorrection, qr.Auto)
	if err != nil {
		return "", fmt.Errorf("totp: encode qr: %w", err)
	}

	size := code.Bounds().Dx()
	return fmt.Sprintf(svgTemplate, size+2*quietZone, darkModulePath(code, size)), nil
}

// darkModulePath renders the dark modules as SVG path data, merging each
// horizontal run into one subpath so the document stays small.
func darkModulePath(code image.Image, size int) string {
	path := make([]byte, 0, size*size/2)
	for y := range size {
		for x := 0; x < size; {
			if !isDark(code, x, y) {
				x++
				continue
			}
			run := 1
			for x+run < size && isDark(code, x+run, y) {
				run++
			}
			path = appendRun(path, x+quietZone, y+quietZone, run)
			x += run
		}
	}
	return string(path)
}

// appendRun writes one horizontal run of dark modules as a closed subpath.
func appendRun(path []byte, x, y, run int) []byte {
	path = append(path, 'M')
	path = strconv.AppendInt(path, int64(x), 10)
	path = append(path, ' ')
	path = strconv.AppendInt(path, int64(y), 10)
	path = append(path, 'h')
	path = strconv.AppendInt(path, int64(run), 10)
	path = append(path, 'v', '1', 'h', '-')
	path = strconv.AppendInt(path, int64(run), 10)
	return append(path, 'z')
}

// isDark reports whether the module at (x, y) is a dark one.
func isDark(code image.Image, x, y int) bool {
	gray, ok := color.GrayModel.Convert(code.At(x, y)).(color.Gray)
	return ok && gray.Y < darkThreshold
}
