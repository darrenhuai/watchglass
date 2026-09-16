package ocr

import (
	"errors"
	"fmt"
)

// ErrNoTesseract is returned by Engines.For when a watch wants tesseract
// and none was found on PATH at boot.
var ErrNoTesseract = errors.New("needs OCR but tesseract is not on PATH; " +
	"install it (e.g. apt install tesseract-ocr / choco install tesseract), " +
	"or set engine: sevenseg if the screen is a seven-segment digit display")

// Engines is the set of recognizers a watchglass process has to offer.
// Tesseract is nil when the binary is not on PATH; SevenSeg is the built-in
// decoder and never missing (nil means the package default).
type Engines struct {
	Tesseract Engine
	SevenSeg  Engine
}

// For resolves a config.Watch.Engine value: "" or "tesseract" is the
// tesseract engine (ErrNoTesseract when there is none), "sevenseg" the
// seven-segment decoder. Anything else is a config error config.Validate
// should already have caught.
func (e Engines) For(name string) (Engine, error) {
	switch name {
	case "", "tesseract":
		if e.Tesseract == nil {
			return nil, ErrNoTesseract
		}
		return e.Tesseract, nil
	case "sevenseg":
		if e.SevenSeg == nil {
			return NewSevenSeg(), nil
		}
		return e.SevenSeg, nil
	}
	return nil, fmt.Errorf("engine %q is not one of tesseract, sevenseg", name)
}
