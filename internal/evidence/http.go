package evidence

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

const MaxBodyBytes = 262144

// ReadResponse bounds native ACK/config bytes before JSON parsing. The caller's
// existing http.Client/context deadline covers connection and streaming reads.
func ReadResponse(response *http.Response) ([]byte, error) {
	encoding := strings.ToLower(strings.TrimSpace(response.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" {
		return nil, errors.New("unsupported native reply encoding")
	}
	if response.ContentLength > MaxBodyBytes {
		return nil, errors.New("native reply exceeds byte limit")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, MaxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > MaxBodyBytes || !utf8.Valid(body) {
		return nil, errors.New("invalid native reply bytes")
	}
	return body, nil
}
