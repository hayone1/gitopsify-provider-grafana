package grafana

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hayone1/gitopsify/sdk"
)

// client talks to one Grafana.
type client struct {
	base  *url.URL
	http  *http.Client
	user  string
	pass  string
	token string
}

func newClient(settings map[string]string) (*client, error) {
	raw := strings.TrimSpace(settings["url"])
	if raw == "" {
		return nil, sdk.Errorf(sdk.CodeInvalidSettings, "url is empty").WithHint("set GRAFANA_URL or the url setting")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, sdk.Errorf(sdk.CodeInvalidSettings, "url %q isn't an http(s) URL", raw)
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	c := &client{base: u}
	switch auth := settings["auth"]; {
	case auth == "" || auth == "anonymous":
	case strings.Contains(auth, ":"):
		c.user, c.pass, _ = strings.Cut(auth, ":")
	default:
		c.token = auth
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
	if v := settings["insecure_skip_verify"]; v != "" {
		skip, err := strconv.ParseBool(v)
		if err != nil {
			return nil, sdk.Errorf(sdk.CodeInvalidSettings, "insecure_skip_verify %q isn't true or false", v)
		}
		tlsConfig.InsecureSkipVerify = skip //nolint:gosec // the user asked for it
	}
	if p := settings["ca_cert"]; p != "" {
		// A path or the PEM text itself, as for the Terraform provider.
		pem := []byte(p)
		if !strings.Contains(p, "-----BEGIN") {
			var err error
			if pem, err = os.ReadFile(p); err != nil {
				return nil, sdk.Errorf(sdk.CodeInvalidSettings, "ca_cert: %v", err)
			}
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, sdk.Errorf(sdk.CodeInvalidSettings, "ca_cert %s holds no PEM certificates", p)
		}
		tlsConfig.RootCAs = pool
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.TLSClientConfig = tlsConfig
	c.http = &http.Client{Transport: tr, Timeout: 60 * time.Second}
	return c, nil
}

// basic reports whether the client uses basic auth, the only way to work
// across organizations.
func (c *client) basic() bool { return c.user != "" }

// get fetches path (with query) in an organization (0: the default one) and
// decodes the JSON answer into out. It retries rate limits and server errors.
func (c *client) get(ctx context.Context, org int64, path string, out any) error {
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt*attempt) * 500 * time.Millisecond):
			}
		}
		retry, err := c.try(ctx, org, path, out)
		if err == nil || !retry {
			return err
		}
		lastErr = err
	}
	return lastErr
}

func (c *client) try(ctx context.Context, org int64, path string, out any) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base.String()+path, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "gitopsify-provider-grafana")
	switch {
	case c.user != "":
		req.SetBasicAuth(c.user, c.pass)
	case c.token != "":
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if org > 0 && c.basic() {
		req.Header.Set("X-Grafana-Org-Id", strconv.FormatInt(org, 10))
	}
	resp, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		var dns *net.DNSError
		hint := "check the URL and that Grafana is reachable from here"
		if errors.As(err, &dns) {
			hint = "the host name doesn't resolve; check the URL"
		}
		var cert *tls.CertificateVerificationError
		if errors.As(err, &cert) {
			hint = "the TLS certificate isn't trusted; set ca_cert (GRAFANA_CA_CERT) to its CA"
			return false, sdk.Errorf(sdk.CodeUnreachable, "GET %s: %v", path, err).WithHint("%s", hint)
		}
		return true, sdk.Errorf(sdk.CodeUnreachable, "GET %s: %v", path, err).WithHint("%s", hint)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return true, sdk.Errorf(sdk.CodeUnreachable, "GET %s: reading the answer: %v", path, err)
	}
	if len(body) > maxBody {
		return false, sdk.Errorf(sdk.CodeInternal, "GET %s: the answer is larger than %d MiB", path, maxBody>>20)
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 && s <= 60 {
			select {
			case <-ctx.Done():
				return false, ctx.Err()
			case <-time.After(time.Duration(s) * time.Second):
			}
		}
		return true, httpError(path, resp.StatusCode, body)
	}
	if resp.StatusCode != http.StatusOK {
		return false, httpError(path, resp.StatusCode, body)
	}
	record(org, path, body)
	if out == nil {
		return false, nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return false, sdk.Errorf(sdk.CodeInternal, "GET %s: unexpected answer: %v", path, err)
	}
	return false, nil
}

// maxBody caps one answer (a dashboard can be large; not unbounded).
const maxBody = 256 << 20

// statusError is a failed request, with its HTTP status.
type statusError struct {
	err    *sdk.Error
	status int
}

func (e *statusError) Error() string { return e.err.Error() }

func httpError(path string, status int, body []byte) error {
	var msg struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &msg)
	text := msg.Message
	if text == "" {
		text = strings.TrimSpace(string(body))
		if len(text) > 200 {
			text = text[:200] + "…"
		}
	}
	e := sdk.Errorf(sdk.CodeForHTTPStatus(status), "GET %s: HTTP %d: %s", path, status, text)
	switch status {
	case http.StatusUnauthorized:
		e = e.WithHint("Grafana refused the credentials; check GRAFANA_AUTH (user:password or a service account token)")
	case http.StatusForbidden:
		e = e.WithHint("the credentials lack a permission; use a server admin's user:password or an Admin service account token")
	}
	return &statusError{err: e, status: status}
}

func (e *statusError) Unwrap() error { return e.err }

func statusOf(err error) int {
	var se *statusError
	if errors.As(err, &se) {
		return se.status
	}
	return 0
}

// paged fetches pages of a list until a short page.
func paged[T any](ctx context.Context, c *client, org int64, path string, size int) ([]T, error) {
	var all []T
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	for page := 1; page < 10000; page++ {
		var items []T
		if err := c.get(ctx, org, fmt.Sprintf("%s%slimit=%d&page=%d", path, sep, size, page), &items); err != nil {
			return nil, err
		}
		all = append(all, items...)
		if len(items) < size {
			break
		}
	}
	return all, nil
}

// RecordEnv names a directory where every answer is saved, so tests can
// replay a real Grafana (see testdata/README.md).
const RecordEnv = "GITOPSIFY_GRAFANA_RECORD"

func record(org int64, path string, body []byte) {
	dir := os.Getenv(RecordEnv)
	if dir == "" {
		return
	}
	p := filepath.Join(dir, fixtureName(org, path))
	_ = os.MkdirAll(filepath.Dir(p), 0o700)
	_ = os.WriteFile(p, body, 0o600) // may hold decrypted secrets
}

// fixtureName is where an answer is recorded: <org>/<path with query>.json.
func fixtureName(org int64, path string) string {
	return filepath.Join(strconv.FormatInt(org, 10), url.PathEscape(path)+".json")
}
