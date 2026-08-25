package ocr

import (
	"context"
	"errors"
	"image"
	"os/exec"
	"reflect"
	"testing"
)

func TestRecognizeTrimsAndPassesArgs(t *testing.T) {
	var gotBin string
	var gotArgs []string
	te := NewTesseract()
	te.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		gotBin = bin
		gotArgs = args
		if len(stdin) == 0 {
			t.Error("expected PNG bytes on stdin")
		}
		return []byte("PRINT COMPLETE\n\n"), nil
	}
	got, err := te.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8)))
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got != "PRINT COMPLETE" {
		t.Errorf("got %q", got)
	}
	if gotBin != "tesseract" {
		t.Errorf("bin = %q", gotBin)
	}
	want := []string{"stdin", "stdout", "--psm", "7"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("args = %v, want %v", gotArgs, want)
	}
}

func TestRecognizePropagatesError(t *testing.T) {
	te := NewTesseract()
	te.run = func(ctx context.Context, bin string, stdin []byte, args ...string) ([]byte, error) {
		return nil, errors.New("boom")
	}
	if _, err := te.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 8, 8))); err == nil {
		t.Fatal("expected error")
	}
}

// Integration test: only runs when a real tesseract binary is installed.
func TestRecognizeRealBinary(t *testing.T) {
	if _, err := exec.LookPath("tesseract"); err != nil {
		t.Skip("tesseract not installed")
	}
	te := NewTesseract()
	// A blank image should OCR to empty text without erroring.
	got, err := te.Recognize(context.Background(), image.NewRGBA(image.Rect(0, 0, 100, 40)))
	if err != nil {
		t.Fatalf("Recognize: %v", err)
	}
	if got != "" {
		t.Logf("blank image read as %q (acceptable)", got)
	}
}
