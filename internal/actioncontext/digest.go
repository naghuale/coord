package actioncontext

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
)

// SHA256File returns a stable digest for the bytes currently stored at path.
func SHA256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}
