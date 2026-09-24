package ocr

import (
	"errors"
	"fmt"
)

// ErrNoTesseract is returned by Engines.For when a watch wants tesseract
// and FindTesseract found none at boot.
var ErrNoTesseract = errors.New("needs OCR but tesseract wasn't found; " +
	"install it (" + TesseractInstall() + ") and restart watchglass, " +
	"point -tesseract at the binary, " +
	"or set engine: sevenseg if the screen is a seven-segment digit display")

// Engines is the set of recognizers a watchglass process has to offer.
// Tesseract is nil when FindTesseract found no binary; SevenSeg is the built-in
// decoder and never missing (nil means the package default); RapidOCR is
// nil when no Python with the rapidocr package was found at boot.
type Engines struct {
	Tesseract Engine
	SevenSeg  Engine
	RapidOCR  Engine
}

// For resolves a config.Watch.Engine value: "" or "tesseract" is the
// tesseract engine (ErrNoTesseract when there is none), "sevenseg" the
// seven-segment decoder, "rapidocr" the RapidOCR engine (ErrNoRapidOCR
// when there is none). Anything else is a config error config.Validate
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
	case "rapidocr":
		if e.RapidOCR == nil {
			return nil, ErrNoRapidOCR
		}
		return e.RapidOCR, nil
	}
	return nil, fmt.Errorf("engine %q is not one of tesseract, sevenseg, rapidocr", name)
}
