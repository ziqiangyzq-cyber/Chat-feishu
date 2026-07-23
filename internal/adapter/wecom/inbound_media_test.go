package wecom

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestDecryptInboundMediaAES256CBCRoundTrip(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := []byte("wecom inbound image payload")
	ciphertext := encryptInboundMediaForTest(t, plaintext, key)

	for _, rawKey := range []string{string(key), base64.RawStdEncoding.EncodeToString(key)} {
		got, err := decryptInboundMedia(ciphertext, rawKey)
		if err != nil {
			t.Fatalf("decrypt with key %q: %v", rawKey, err)
		}
		if !bytes.Equal(got, plaintext) {
			t.Fatalf("plaintext = %q, want %q", got, plaintext)
		}
	}
}

func TestDownloadAndStageInboundImageUsesSharedStagingDirectory(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	plaintext := append([]byte("\x89PNG\r\n\x1a\n"), []byte("decrypted-image")...)
	ciphertext := encryptInboundMediaForTest(t, plaintext, key)
	tempDir := t.TempDir()
	ch := NewChannel(Config{TempDir: tempDir})
	ch.inboundHTTPClient = &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://example.test/encrypted-image" {
			t.Fatalf("unexpected media URL: %s", request.URL)
		}
		return &http.Response{
			StatusCode:    http.StatusOK,
			Status:        "200 OK",
			Body:          io.NopCloser(bytes.NewReader(ciphertext)),
			ContentLength: int64(len(ciphertext)),
			Header:        make(http.Header),
			Request:       request,
		}, nil
	})}

	got, err := ch.downloadAndStageInboundMedia(
		t.Context(),
		"image",
		"https://example.test/encrypted-image",
		string(key),
	)
	if err != nil {
		t.Fatalf("download and stage image: %v", err)
	}
	if filepath.Dir(got.LocalPath) != tempDir || got.MIMEType != "image/png" {
		t.Fatalf("unexpected staged image: %+v", got)
	}
	staged, err := os.ReadFile(got.LocalPath)
	if err != nil {
		t.Fatalf("read staged image: %v", err)
	}
	if !bytes.Equal(staged, plaintext) {
		t.Fatalf("staged image = %q, want %q", staged, plaintext)
	}
}

func TestDecryptInboundMediaRejectsInvalidPadding(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("create AES cipher: %v", err)
	}
	plaintext := bytes.Repeat([]byte{'x'}, aes.BlockSize)
	plaintext[len(plaintext)-1] = 0
	ciphertext := make([]byte, len(plaintext))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(ciphertext, plaintext)

	if _, err := decryptInboundMedia(ciphertext, string(key)); err == nil {
		t.Fatal("expected invalid padding error")
	}
}

func encryptInboundMediaForTest(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()
	padded := padPKCS7ForTest(plaintext, pkcs7PaddingBlockSize)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("create AES cipher: %v", err)
	}
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:aes.BlockSize]).CryptBlocks(ciphertext, padded)
	return ciphertext
}

func padPKCS7ForTest(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	return append(append([]byte(nil), data...), bytes.Repeat([]byte{byte(padding)}, padding)...)
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}
