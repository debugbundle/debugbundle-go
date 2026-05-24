package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

type FileTransport struct {
	root     string
	mu       sync.Mutex
	sequence uint64
}

var invalidServiceNameCharacters = regexp.MustCompile(`[^A-Za-z0-9._-]+`)
var randomReader io.Reader = rand.Reader

func NewFileTransport(root string) (*FileTransport, error) {
	validated, err := validateRoot(root)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(validated, 0o700); err != nil {
		return nil, err
	}
	if err := ensureDirectoryMode(validated); err != nil {
		return nil, err
	}
	return &FileTransport{root: validated}, nil
}

func (transport *FileTransport) Send(ctx context.Context, request Request) (Response, error) {
	select {
	case <-ctx.Done():
		return Response{}, ctx.Err()
	default:
	}

	payload, err := json.Marshal(request.Events)
	if err != nil {
		return Response{}, err
	}
	timestampMillis, sequence, serviceName := transport.nextFileMetadata(request.Events)
	finalPath := filepath.Join(transport.root, fmt.Sprintf("%d-%d-%s.events.json", timestampMillis, sequence, serviceName))
	token, err := randomToken(8)
	if err != nil {
		return Response{}, err
	}
	tempPath := finalPath + ".tmp-" + token
	file, err := os.OpenFile(tempPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|openNoFollowFlag, 0o600)
	if err != nil {
		return Response{}, err
	}
	if _, err := file.Write(payload); err != nil {
		file.Close()
		_ = os.Remove(tempPath)
		return Response{}, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tempPath)
		return Response{}, err
	}
	if info, err := os.Lstat(finalPath); err == nil && info.Mode()&os.ModeSymlink != 0 {
		_ = os.Remove(tempPath)
		return Response{}, fmt.Errorf("refusing to write through symlink target")
	}
	if err := os.Rename(tempPath, finalPath); err != nil {
		_ = os.Remove(tempPath)
		return Response{}, err
	}
	return Response{StatusCode: 202, WrittenFilePath: finalPath}, nil
}

func (transport *FileTransport) nextFileMetadata(events []json.RawMessage) (int64, uint64, string) {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	transport.sequence++
	return time.Now().UTC().UnixMilli(), transport.sequence, inferServiceName(events)
}

func inferServiceName(events []json.RawMessage) string {
	if len(events) == 0 {
		return "service"
	}
	var payload map[string]any
	if err := json.Unmarshal(events[0], &payload); err != nil {
		return "service"
	}
	switch service := payload["service"].(type) {
	case string:
		return sanitizeServiceName(service)
	case map[string]any:
		if name, ok := service["name"].(string); ok {
			return sanitizeServiceName(name)
		}
	case map[string]string:
		return sanitizeServiceName(service["name"])
	}
	return "service"
}

func sanitizeServiceName(serviceName string) string {
	normalized := invalidServiceNameCharacters.ReplaceAllString(strings.TrimSpace(serviceName), "-")
	normalized = strings.Trim(normalized, "-")
	normalized = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(normalized, "--", "-"), "--", "-"))
	if normalized == "" {
		return "service"
	}
	return normalized
}

func validateRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("events directory is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	for current := abs; current != string(filepath.Separator); current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return "", fmt.Errorf("symlinked events directory is not allowed")
			}
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
	}
	return abs, nil
}

func ensureDirectoryMode(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("events directory must be a directory")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("events directory cannot be a symlink")
	}
	return os.Chmod(path, 0o700)
}

func randomToken(byteCount int) (string, error) {
	buffer := make([]byte, byteCount)
	if _, err := io.ReadFull(randomReader, buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}
