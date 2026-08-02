package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/sozercan/d365-expense-cli/internal/expense"
)

func receiptInputFromPath(path string, maxSize int64) (expense.ReceiptInput, error) {
	name := filepath.Base(path)
	if strings.TrimSpace(path) == "" || name == "." || name == string(filepath.Separator) {
		return expense.ReceiptInput{}, errors.New("receipt file path is empty")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return expense.ReceiptInput{}, fmt.Errorf("stat receipt %q: %w", name, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q is not a regular non-symlink file", name)
	}
	if info.Size() <= 0 {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q is empty", name)
	}
	if maxSize <= 0 || info.Size() > maxSize {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q size %d exceeds limit %d", name, info.Size(), maxSize)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q permissions %04o are too broad; use chmod 600", name, info.Mode().Perm())
	}
	if !strings.EqualFold(filepath.Ext(name), ".png") {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q must have a .png extension", name)
	}

	file, err := os.Open(path)
	if err != nil {
		return expense.ReceiptInput{}, fmt.Errorf("open receipt %q: %w", name, err)
	}
	validatedBytes, readErr := io.ReadAll(io.LimitReader(file, maxSize+1))
	closeErr := file.Close()
	if readErr != nil {
		return expense.ReceiptInput{}, fmt.Errorf("read receipt %q: %w", name, readErr)
	}
	if closeErr != nil {
		return expense.ReceiptInput{}, fmt.Errorf("close receipt %q: %w", name, closeErr)
	}
	if int64(len(validatedBytes)) != info.Size() || int64(len(validatedBytes)) > maxSize {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q changed while being validated", name)
	}
	prefixLength := min(len(validatedBytes), 512)
	mediaType := http.DetectContentType(validatedBytes[:prefixLength])
	if mediaType != "image/png" {
		return expense.ReceiptInput{}, fmt.Errorf("receipt %q content type %q is not image/png", name, mediaType)
	}

	return expense.ReceiptInput{
		Filename:  name,
		MediaType: mediaType,
		Size:      info.Size(),
		Open: func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(validatedBytes)), nil
		},
	}, nil
}
