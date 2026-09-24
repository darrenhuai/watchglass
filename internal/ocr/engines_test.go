package ocr

import (
	"context"
	"errors"
	"image"
	"strings"
	"testing"
)

type stubEngine struct{ out string }

func (s stubEngine) Recognize(ctx context.Context, img image.Image) (string, error) {
	return s.out, nil
}

func TestEnginesForDefaultsToTesseract(t *testing.T) {
	tess := stubEngine{out: "tess"}
	e := Engines{Tesseract: tess}
	for _, name := range []string{"", "tesseract"} {
		got, err := e.For(name)
		if err != nil {
			t.Fatalf("For(%q): %v", name, err)
		}
		if got != tess {
			t.Errorf("For(%q) = %#v, want the tesseract engine", name, got)
		}
	}
}

func TestEnginesForWithoutTesseractSaysHowToGetIt(t *testing.T) {
	var e Engines
	for _, name := range []string{"", "tesseract"} {
		got, err := e.For(name)
		if err == nil {
			t.Fatalf("For(%q) with no tesseract returned %#v, want an error", name, got)
		}
		for _, want := range []string{"needs OCR but tesseract wasn't found", TesseractInstall(), "restart watchglass", "-tesseract"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("For(%q) error %q should say %q", name, err, want)
			}
		}
		if !strings.Contains(err.Error(), "sevenseg") {
			t.Errorf("For(%q) error %q should point at the built-in decoder as the way out", name, err)
		}
		if !errors.Is(err, ErrNoTesseract) {
			t.Errorf("For(%q) error is not ErrNoTesseract: %v", name, err)
		}
	}
}

func TestEnginesForSevenSegAlwaysPresent(t *testing.T) {
	var e Engines // zero value: nothing set at all
	got, err := e.For("sevenseg")
	if err != nil {
		t.Fatalf("For(sevenseg): %v", err)
	}
	if _, ok := got.(*SevenSeg); !ok {
		t.Errorf("For(sevenseg) = %T, want the built-in *SevenSeg", got)
	}
	custom := stubEngine{out: "custom"}
	e.SevenSeg = custom
	if got, _ := e.For("sevenseg"); got != custom {
		t.Errorf("an explicitly set SevenSeg must win, got %#v", got)
	}
}

func TestEnginesForRejectsUnknown(t *testing.T) {
	e := Engines{Tesseract: stubEngine{}}
	for _, name := range []string{"bogus", "SevenSeg", "Tesseract "} {
		if got, err := e.For(name); err == nil {
			t.Errorf("For(%q) = %#v, want an error", name, got)
		} else if !strings.Contains(err.Error(), name) {
			t.Errorf("For(%q) error %q does not name the bad engine", name, err)
		}
	}
}

func TestEnginesForRapidOCR(t *testing.T) {
	var e Engines
	got, err := e.For("rapidocr")
	if err == nil {
		t.Fatalf("For(rapidocr) with none detected returned %#v, want an error", got)
	}
	if !errors.Is(err, ErrNoRapidOCR) {
		t.Errorf("For(rapidocr) error is not ErrNoRapidOCR: %v", err)
	}
	for _, want := range []string{"rapidocr", "pip install rapidocr onnxruntime", "python3", "-python"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("For(rapidocr) error %q should mention %q", err, want)
		}
	}
	rapid := stubEngine{out: "rapid"}
	e.RapidOCR = rapid
	if got, err := e.For("rapidocr"); err != nil || got != rapid {
		t.Errorf("For(rapidocr) = %#v, %v; want the rapidocr engine", got, err)
	}
	// rapidocr being present changes nothing for the other spellings.
	if _, err := e.For(""); !errors.Is(err, ErrNoTesseract) {
		t.Errorf("For(\"\") with only rapidocr set = %v, want ErrNoTesseract", err)
	}
}

func TestEnginesForUnknownListsAllThree(t *testing.T) {
	_, err := Engines{}.For("bogus")
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "tesseract, sevenseg, rapidocr") {
		t.Errorf("error %q should list all three engines", err)
	}
}
