package wecom

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/kxn/codex-remote-feishu/internal/inboundmedia"
)

const (
	maxInboundMediaBytes      = 100 << 20
	pkcs7PaddingBlockSize     = 32
	maxInboundCiphertextBytes = maxInboundMediaBytes + pkcs7PaddingBlockSize
)

type stagedInboundMedia struct {
	LocalPath string
	FileName  string
	MIMEType  string
}

func (c *Channel) downloadAndStageInboundMedia(ctx context.Context, msgType, rawURL, rawAESKey string) (stagedInboundMedia, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return stagedInboundMedia{}, errors.New("missing media URL")
	}
	if strings.TrimSpace(rawAESKey) == "" {
		return stagedInboundMedia{}, errors.New("missing media AES key")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return stagedInboundMedia{}, fmt.Errorf("build media request: %w", err)
	}
	response, err := c.inboundHTTPClient.Do(request)
	if err != nil {
		return stagedInboundMedia{}, fmt.Errorf("download media: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return stagedInboundMedia{}, fmt.Errorf("download media: unexpected HTTP status %s", response.Status)
	}
	if response.ContentLength > maxInboundCiphertextBytes {
		return stagedInboundMedia{}, fmt.Errorf("download media: encrypted payload exceeds %d bytes", maxInboundCiphertextBytes)
	}
	ciphertext, err := io.ReadAll(io.LimitReader(response.Body, maxInboundCiphertextBytes+1))
	if err != nil {
		return stagedInboundMedia{}, fmt.Errorf("read media ciphertext: %w", err)
	}
	if len(ciphertext) > maxInboundCiphertextBytes {
		return stagedInboundMedia{}, fmt.Errorf("download media: encrypted payload exceeds %d bytes", maxInboundCiphertextBytes)
	}
	plaintext, err := decryptInboundMedia(ciphertext, rawAESKey)
	if err != nil {
		return stagedInboundMedia{}, fmt.Errorf("decrypt media: %w", err)
	}
	if len(plaintext) > maxInboundMediaBytes {
		return stagedInboundMedia{}, fmt.Errorf("decrypted media exceeds %d bytes", maxInboundMediaBytes)
	}

	switch strings.ToLower(strings.TrimSpace(msgType)) {
	case "image":
		localPath, mimeType, err := inboundmedia.StageImage(c.config.TempDir, plaintext)
		if err != nil {
			return stagedInboundMedia{}, fmt.Errorf("stage image: %w", err)
		}
		return stagedInboundMedia{LocalPath: localPath, MIMEType: mimeType}, nil
	case "file":
		fileName := inboundFileName(rawURL)
		localPath, err := inboundmedia.StageFile(c.config.TempDir, fileName, bytes.NewReader(plaintext))
		if err != nil {
			return stagedInboundMedia{}, fmt.Errorf("stage file: %w", err)
		}
		return stagedInboundMedia{LocalPath: localPath, FileName: fileName}, nil
	default:
		return stagedInboundMedia{}, fmt.Errorf("unsupported inbound media type %q", msgType)
	}
}

func decryptInboundMedia(ciphertext []byte, rawAESKey string) ([]byte, error) {
	key, err := decodeInboundMediaAESKey(rawAESKey)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("ciphertext length %d is not a positive AES block multiple", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plaintext, ciphertext)
	return removePKCS7Padding(plaintext, pkcs7PaddingBlockSize)
}

func decodeInboundMediaAESKey(rawAESKey string) ([]byte, error) {
	trimmed := strings.TrimSpace(rawAESKey)
	if len(trimmed) == 32 {
		return []byte(trimmed), nil
	}
	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	for _, encoding := range encodings {
		decoded, err := encoding.DecodeString(trimmed)
		if err == nil && len(decoded) == 32 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("AES key must be 32 raw bytes or base64-encoded 32 bytes")
}

func removePKCS7Padding(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || blockSize <= 0 || len(data)%blockSize != 0 {
		return nil, errors.New("invalid padded plaintext length")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, errors.New("invalid PKCS#7 padding length")
	}
	for _, value := range data[len(data)-padding:] {
		if int(value) != padding {
			return nil, errors.New("invalid PKCS#7 padding bytes")
		}
	}
	return append([]byte(nil), data[:len(data)-padding]...), nil
}

func inboundFileName(rawURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return "attachment.bin"
	}
	name := strings.TrimSpace(path.Base(parsed.Path))
	if name == "" || name == "." || name == "/" {
		return "attachment.bin"
	}
	return name
}

func newInboundMediaHTTPClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second}
}
