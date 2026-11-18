package msfp

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
)

// JPEGSheets is the default SheetEncoder: it decodes a sheet with
// image/jpeg and encodes its pixels again, so that nothing but pixels
// (no metadata, no trailing bytes, no second image) reaches the viewer
// (MSFP video section 7). The engine has checked the sheet's size and
// its header first; Reencode checks the header again, then decodes.
type JPEGSheets struct{}

// ErrNotAnImage is a sheet that isn't a JPEG within the layout's size.
var ErrNotAnImage = &Error{Code: "NotAnImage"}

// Reencode decodes a JPEG no larger than maxWidth by maxHeight and
// encodes it again.
func (JPEGSheets) Reencode(sheet []byte, maxWidth, maxHeight int) ([]byte, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(sheet))
	if err != nil || format != "jpeg" || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > maxWidth || cfg.Height > maxHeight {
		return nil, ErrNotAnImage.with(errors.New("not a JPEG within the layout"))
	}
	img, err := jpeg.Decode(bytes.NewReader(sheet))
	if err != nil {
		return nil, ErrNotAnImage.with(err)
	}
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 85}); err != nil {
		return nil, ErrNotAnImage.with(err)
	}
	return out.Bytes(), nil
}

// sheetFits checks a sheet from its header, before anything decodes it:
// a JPEG (by its content, whatever its Content-Type) no larger than the
// layout's full sheet (MSFP video section 6.2).
func sheetFits(sheet []byte, maxWidth, maxHeight int) bool {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(sheet))
	return err == nil && format == "jpeg" && cfg.Width >= 1 && cfg.Height >= 1 && cfg.Width <= maxWidth && cfg.Height <= maxHeight
}
