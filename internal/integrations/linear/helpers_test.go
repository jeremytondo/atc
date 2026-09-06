package linear

import (
	"bytes"
	"io"
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/webhooks"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func withMethod(req *http.Request, method string) *http.Request {
	req.Method = method
	return req
}

func withSignature(req *http.Request, signature string) *http.Request {
	req.Header.Set(signatureHeader, signature)
	return req
}

// tamper flips a byte of the body after signing.
func tamper(req *http.Request) *http.Request {
	body, _ := io.ReadAll(req.Body)
	body = append(body[:len(body)-2], ' ', body[len(body)-1])
	req.Body = io.NopCloser(bytes.NewReader(body))
	req.ContentLength = int64(len(body))
	return req
}

func removeFile(path string) error { return os.Remove(path) }

// accepted wraps a payload as the inbox hands it to Process.
func accepted(body []byte) webhooks.Accepted {
	return webhooks.Accepted{ID: "whk-1", DeliveryID: "dlv-e2e", Payload: body}
}

func canonical(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := paths.CanonicalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
