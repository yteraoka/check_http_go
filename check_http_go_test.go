package main

import (
	"bytes"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// main() calls os.Exit, so each test case re-executes this test binary as
// check_http_go and inspects its stdout and exit code.
const runMainEnv = "CHECK_HTTP_GO_RUN_MAIN"

func TestMain(m *testing.M) {
	if os.Getenv(runMainEnv) == "1" {
		os.Args = append([]string{"check_http_go"}, os.Args[1:]...)
		main()
		return
	}
	os.Exit(m.Run())
}

func runCheck(t *testing.T, args ...string) (int, string) {
	t.Helper()
	code, stdout, _ := runCheckWithStderr(t, args...)
	return code, stdout
}

func runCheckWithStderr(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], args...)
	// GORACE: skip the 1s sleep at exit when the test binary is built with -race
	cmd.Env = append(os.Environ(), runMainEnv+"=1", "GORACE=atexit_sleep_ms=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("failed to run check_http_go: %s", err)
		}
		return exitErr.ExitCode(), string(out), stderr.String()
	}
	return 0, string(out), stderr.String()
}

// receive waits for a value sent from a test server handler. It fails instead
// of blocking forever when the check exits before sending a request.
func receive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("the test server did not receive a request")
	}
	var zero T
	return zero
}

// serverArgs returns -I and -p arguments pointing to srv
func serverArgs(t *testing.T, srv *httptest.Server) []string {
	t.Helper()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return []string{"-I", u.Hostname(), "-p", u.Port()}
}

func assertResult(t *testing.T, gotCode int, gotOut string, wantCode int, wantOut string) {
	t.Helper()
	if gotCode != wantCode {
		t.Errorf("exit code = %d, want %d\noutput:\n%s", gotCode, wantCode, gotOut)
	}
	if !strings.Contains(gotOut, wantOut) {
		t.Errorf("output does not contain %q\noutput:\n%s", wantOut, gotOut)
	}
}

func TestStatusCode(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		code, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if err != nil {
			t.Errorf("invalid path: %s", r.URL.Path)
			return
		}
		w.WriteHeader(code)
	}))
	defer srv.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"200", []string{"-u", "/200"}, NagiosOk, "HTTP OK: HTTP/1.1 200 OK"},
		// redirects are not followed
		{"301", []string{"-u", "/301"}, NagiosOk, "HTTP OK: HTTP/1.1 301 Moved Permanently"},
		{"404", []string{"-u", "/404"}, NagiosWarning, "Unexpected http status code: 404"},
		{"503", []string{"-u", "/503"}, NagiosCritical, "Unexpected http status code: 503"},
		{"expect matched", []string{"-u", "/404", "-e", "200,404"}, NagiosOk, "HTTP OK: HTTP/1.1 404 Not Found"},
		// check_http returns CRITICAL in this case (#16)
		{"expect unmatched", []string{"-u", "/200", "-e", "201,204"}, NagiosCritical, "Unexpected http status code: 200"},
		{"expect with spaces", []string{"-u", "/301", "-e", "200, 301"}, NagiosOk, "HTTP OK: HTTP/1.1 301 Moved Permanently"},
		// non 3-digit values are searched for in the status line as check_http does
		{"expect status line", []string{"-u", "/200", "-e", "HTTP/1.1 200"}, NagiosOk, "HTTP OK: HTTP/1.1 200 OK"},
		{"expect status line unmatched", []string{"-u", "/200", "-e", "HTTP/1.0 200,Created"}, NagiosCritical, "Unexpected http status code: 200"},
		// a 3-digit value is not a substring match against the status line
		{"expect code is exact", []string{"-u", "/200", "-e", "100"}, NagiosCritical, "Unexpected http status code: 200"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(serverArgs(t, srv), tt.args...)...)
			assertResult(t, code, out, tt.wantCode, tt.wantOut)
		})
	}
}

func TestRequest(t *testing.T) {
	reqs := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- r.Clone(r.Context())
	}))
	defer srv.Close()

	t.Run("default", func(t *testing.T) {
		code, out := runCheck(t, serverArgs(t, srv)...)
		assertResult(t, code, out, NagiosOk, "HTTP OK")
		got := receive(t, reqs)
		u, _ := url.Parse(srv.URL)
		if got.Method != http.MethodGet || got.URL.Path != "/" || got.Host != u.Hostname() || got.UserAgent() != "check_http_go" {
			t.Errorf("unexpected request: method=%s path=%s host=%s user-agent=%s", got.Method, got.URL.Path, got.Host, got.UserAgent())
		}
	})

	t.Run("options", func(t *testing.T) {
		args := append(serverArgs(t, srv),
			"-H", "www.example.com",
			"-u", "/health?q=1",
			"-j", "HEAD",
			"-A", "test-agent",
			"-k", "X-Foo: bar",
			"-k", "X-Baz: qux",
		)
		code, out := runCheck(t, args...)
		assertResult(t, code, out, NagiosOk, "HTTP OK")
		got := receive(t, reqs)
		if got.Method != http.MethodHead {
			t.Errorf("method = %s, want HEAD", got.Method)
		}
		if got.URL.RequestURI() != "/health?q=1" {
			t.Errorf("request uri = %s, want /health?q=1", got.URL.RequestURI())
		}
		if got.Host != "www.example.com" {
			t.Errorf("host = %s, want www.example.com", got.Host)
		}
		if got.UserAgent() != "test-agent" {
			t.Errorf("user-agent = %s, want test-agent", got.UserAgent())
		}
		if got.Header.Get("X-Foo") != "bar" || got.Header.Get("X-Baz") != "qux" {
			t.Errorf("headers = %v, want X-Foo: bar and X-Baz: qux", got.Header)
		}
	})
}

func TestBuildURL(t *testing.T) {
	tests := []struct {
		host string
		port int
		uri  string
		want string
	}{
		{"127.0.0.1", 80, "/", "http://127.0.0.1:80/"},
		{"::1", 8080, "/health", "http://[::1]:8080/health"},
		// the zone of an IPv6 address is escaped
		{"fe80::1%eth0", 80, "/", "http://[fe80::1%25eth0]:80/"},
		// the URI always starts with "/"
		{"127.0.0.1", 80, "health", "http://127.0.0.1:80/health"},
		{"127.0.0.1", 80, "?q=1", "http://127.0.0.1:80/?q=1"},
		{"127.0.0.1", 80, "", "http://127.0.0.1:80/"},
	}
	for _, tt := range tests {
		if got := buildURL("http", tt.host, tt.port, tt.uri); got != tt.want {
			t.Errorf("buildURL(%q, %d, %q) = %q, want %q", tt.host, tt.port, tt.uri, got, tt.want)
		}
	}
}

func TestHostHeader(t *testing.T) {
	for host, want := range map[string]string{
		"www.example.com":      "www.example.com",
		"www.example.com:8080": "www.example.com:8080",
		"127.0.0.1":            "127.0.0.1",
		"::1":                  "[::1]",
		"[::1]":                "[::1]",
		"[::1]:8080":           "[::1]:8080",
	} {
		if got := hostHeader(host); got != want {
			t.Errorf("hostHeader(%q) = %q, want %q", host, got, want)
		}
	}
}

// IPv6 addresses work with -I and -H (#12)
func TestIPv6(t *testing.T) {
	l, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		t.Skipf("IPv6 is not available: %s", err)
	}
	hosts := make(chan string, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hosts <- r.Host
	}))
	// replace the IPv4 listener created by NewUnstartedServer
	if err := srv.Listener.Close(); err != nil {
		t.Fatal(err)
	}
	srv.Listener = l
	srv.Start()
	defer srv.Close()
	port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)

	tests := []struct {
		name     string
		args     []string
		wantHost string
	}{
		{"-I without brackets", []string{"-I", "::1", "-p", port}, "[::1]"},
		{"-I with brackets", []string{"-I", "[::1]", "-p", port}, "[::1]"},
		{"-H with port", []string{"-H", "[::1]:" + port}, "[::1]:" + port},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, tt.args...)
			assertResult(t, code, out, NagiosOk, "HTTP OK")
			if got := receive(t, hosts); got != tt.wantHost {
				t.Errorf("Host = %q, want %q", got, tt.wantHost)
			}
		})
	}
}

// the port in -H is used to connect to unless -I or -p is given, as check_http does (#12)
func TestVhostWithPort(t *testing.T) {
	type request struct{ host, uri string }
	reqs := make(chan request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs <- request{r.Host, r.URL.RequestURI()}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	tests := []struct {
		name string
		args []string
		want request
	}{
		{"port from -H", []string{"-H", u.Host}, request{u.Host, "/"}},
		{"-p wins over -H", []string{"-H", u.Hostname() + ":1", "-p", u.Port()}, request{u.Hostname() + ":1", "/"}},
		{"URI without slash", []string{"-H", u.Host, "-u", "health?q=1"}, request{u.Host, "/health?q=1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, tt.args...)
			assertResult(t, code, out, NagiosOk, "HTTP OK")
			if got := receive(t, reqs); got != tt.want {
				t.Errorf("request = %+v, want %+v", got, tt.want)
			}
		})
	}

	t.Run("invalid port in -H", func(t *testing.T) {
		code, out := runCheck(t, "-H", "www.example.com:http")
		assertResult(t, code, out, NagiosUnknown, "HTTP UNKNOWN - invalid port in -H: www.example.com:http")
	})
}

func TestJSON(t *testing.T) {
	bodies := map[string]string{
		"/object": `{
			"xxx": {"status": "ok"},
			"int": 1, "float": 1.50, "big": 12345678901234567890,
			"true": true, "null": null, "empty": "", "html": {"a": "<b>&"},
			"items": [{"status": "ok"}, {"status": "ng"}]
		}`,
		"/array":    `[{"status": "ok"}]`,
		"/invalid":  `<html>not json</html>`,
		"/trailing": `{"status": "ok"} {}`,
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, bodies[r.URL.Path])
	}))
	defer srv.Close()

	tests := []struct {
		name     string
		uri      string
		key      string
		value    string
		wantCode int
		wantOut  string
	}{
		{"matched", "/object", "xxx.status", "ok", NagiosOk, "HTTP OK"},
		{"unmatched", "/object", "xxx.status", "ng", NagiosCritical, "`xxx.status` is not `ng` (actual: `ok`)"},
		{"missing key", "/object", "yyy.status", "ok", NagiosCritical, "`yyy.status` is not found in the response body"},
		{"key under a string", "/object", "xxx.status.foo", "ok", NagiosCritical, "`xxx.status.foo` is not found in the response body"},
		// non-string values are compared with their JSON representation (#11)
		{"integer", "/object", "int", "1", NagiosOk, "HTTP OK"},
		{"float as written", "/object", "float", "1.50", NagiosOk, "HTTP OK"},
		{"big integer without precision loss", "/object", "big", "12345678901234567890", NagiosOk, "HTTP OK"},
		{"boolean", "/object", "true", "true", NagiosOk, "HTTP OK"},
		{"null", "/object", "null", "null", NagiosOk, "HTTP OK"},
		{"empty string", "/object", "empty", "", NagiosOk, "HTTP OK"},
		{"object", "/object", "xxx", `{"status":"ok"}`, NagiosOk, "HTTP OK"},
		{"object without HTML escaping", "/object", "html", `{"a":"<b>&"}`, NagiosOk, "HTTP OK"},
		{"number unmatched", "/object", "int", "2", NagiosCritical, "`int` is not `2` (actual: `1`)"},
		// array elements are referred to by index
		{"array index", "/object", "items.1.status", "ng", NagiosOk, "HTTP OK"},
		{"array index out of range", "/object", "items.2.status", "ok", NagiosCritical, "`items.2.status` is not found in the response body"},
		{"array non-numeric index", "/object", "items.x", "ok", NagiosCritical, "`items.x` is not found in the response body"},
		{"top-level array", "/array", "0.status", "ok", NagiosOk, "HTTP OK"},
		{"invalid JSON", "/invalid", "status", "ok", NagiosCritical, "response body is not valid JSON: "},
		{"trailing data", "/trailing", "status", "ok", NagiosCritical, "response body is not valid JSON: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append(serverArgs(t, srv), "-u", tt.uri, "--json-key", tt.key, "--json-value="+tt.value)
			code, out := runCheck(t, args...)
			assertResult(t, code, out, tt.wantCode, tt.wantOut)
		})
	}

	t.Run("key without value", func(t *testing.T) {
		code, out := runCheck(t, append(serverArgs(t, srv), "-u", "/object", "--json-key", "xxx.status")...)
		assertResult(t, code, out, NagiosUnknown, "HTTP UNKNOWN - --json-key and --json-value must be specified together")
	})
	t.Run("value without key", func(t *testing.T) {
		code, out := runCheck(t, append(serverArgs(t, srv), "-u", "/object", "--json-value", "ok")...)
		assertResult(t, code, out, NagiosUnknown, "HTTP UNKNOWN - --json-key and --json-value must be specified together")
	})
	t.Run("empty key", func(t *testing.T) {
		code, out := runCheck(t, append(serverArgs(t, srv), "-u", "/object", "--json-key=", "--json-value", "ok")...)
		assertResult(t, code, out, NagiosUnknown, "HTTP UNKNOWN - --json-key must not be empty")
	})
}

func TestResponseTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer srv.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"ok", []string{"-w", "5", "-c", "10"}, NagiosOk, "HTTP OK"},
		{"warning", []string{"-w", "0.1", "-c", "10"}, NagiosWarning, "exceeded warning threshold 0.100s"},
		{"critical", []string{"-w", "0.05", "-c", "0.1"}, NagiosCritical, "exceeded critical threshold 0.100s"},
		{"warn greater than crit", []string{"-w", "10", "-c", "5"}, NagiosUnknown, "HTTP UNKNOWN - warning threshold 10.000s must not be greater than critical threshold 5.000s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(serverArgs(t, srv), tt.args...)...)
			assertResult(t, code, out, tt.wantCode, tt.wantOut)
		})
	}
}

// all failure reasons are reported on the first line and the most severe status wins (#16, #17)
func TestMultipleReasons(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprint(w, `{"xxx":{"status":"ng"}}`)
	}))
	defer srv.Close()

	code, out := runCheck(t, append(serverArgs(t, srv), "-w", "0.1", "--json-key", "xxx.status", "--json-value", "ok")...)
	if code != NagiosCritical {
		t.Errorf("exit code = %d, want %d\noutput:\n%s", code, NagiosCritical, out)
	}
	firstLine, longOutput, _ := strings.Cut(out, "\n")
	re := regexp.MustCompile("^HTTP CRITICAL: HTTP/1\\.1 404 Not Found - " +
		"Unexpected http status code: 404; " +
		"`xxx\\.status` is not `ok` \\(actual: `ng`\\); " +
		"response time [0-9.]+s exceeded warning threshold 0\\.100s - " +
		"23 bytes in [0-9.]+ second response time \\|")
	if !re.MatchString(firstLine) {
		t.Errorf("first line %q does not match %s", firstLine, re)
	}
	// the JSON body follows as the long output when the JSON check fails
	wantLong := "\n{\n    \"xxx\": {\n        \"status\": \"ng\"\n    }\n}"
	if longOutput != wantLong {
		t.Errorf("long output = %q, want %q", longOutput, wantLong)
	}
}

// the JSON body is not printed when the JSON check passes (#17)
func TestJSONBodyOnSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"xxx":{"status":"ok"}}`)
	}))
	defer srv.Close()

	code, out := runCheck(t, append(serverArgs(t, srv), "--json-key", "xxx.status", "--json-value", "ok")...)
	if code != NagiosOk || strings.Count(out, "\n") != 1 {
		t.Errorf("exit code = %d, want a single line output\noutput:\n%s", code, out)
	}
}

// "|" and line breaks in a reason would break the first line and the perfdata
func TestReasonEscaping(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"s":"a|b\r\nc"}`)
	}))
	defer srv.Close()

	code, out := runCheck(t, append(serverArgs(t, srv), "--json-key", "s", "--json-value", "x")...)
	firstLine, _, _ := strings.Cut(out, "\n")
	want := "HTTP CRITICAL: HTTP/1.1 200 OK - `s` is not `x` (actual: `a¦b\\r\\nc`) - "
	if code != NagiosCritical || !strings.HasPrefix(firstLine, want) || strings.Count(firstLine, "|") != 1 {
		t.Errorf("exit code = %d, first line %q does not start with %q or has extra \"|\"", code, firstLine, want)
	}
}

func TestTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	code, out := runCheck(t, append(serverArgs(t, srv), "-t", "1")...)
	assertResult(t, code, out, NagiosCritical, "HTTP CRITICAL - ")
}

func TestConnectionRefused(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
	if err := l.Close(); err != nil {
		t.Fatal(err)
	}

	code, out := runCheck(t, "-I", "127.0.0.1", "-p", port)
	assertResult(t, code, out, NagiosCritical, "connection refused")
}

func TestTLS(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	code, out := runCheck(t, append(serverArgs(t, srv), "-S")...)
	assertResult(t, code, out, NagiosOk, "HTTP OK: HTTP/2.0 200 OK")
}

// -S with -p must not fall back to plain HTTP (#9)
func TestTLSAgainstPlainHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	code, out := runCheck(t, append(serverArgs(t, srv), "-S")...)
	assertResult(t, code, out, NagiosCritical, "HTTP CRITICAL - ")
}

// the host name given by -H is sent as SNI (#15)
func TestSNI(t *testing.T) {
	serverNames := make(chan string, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.TLS = &tls.Config{
		GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
			serverNames <- hello.ServerName
			return nil, nil
		},
	}
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"vhost", []string{"-H", "www.example.com"}, "www.example.com"},
		{"vhost with port", []string{"-H", "www.example.com:8443"}, "www.example.com"},
		// an IP address is not sent as SNI
		{"no vhost", nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(append(serverArgs(t, srv), "-S"), tt.args...)...)
			assertResult(t, code, out, NagiosOk, "HTTP OK: HTTP/2.0 200 OK")
			if got := receive(t, serverNames); got != tt.want {
				t.Errorf("SNI = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTLSServerName(t *testing.T) {
	for host, want := range map[string]string{
		"www.example.com":      "www.example.com",
		"www.example.com:8443": "www.example.com",
		"127.0.0.1":            "127.0.0.1",
		"127.0.0.1:8443":       "127.0.0.1",
		"::1":                  "::1",
		"[::1]":                "::1",
		"[::1]:8443":           "::1",
	} {
		if got := tlsServerName(host); got != want {
			t.Errorf("tlsServerName(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestVerifyCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	// the httptest certificate is a self-signed CA for 127.0.0.1, example.com and *.example.com
	dir := t.TempDir()
	caCert := filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(caCert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	notPEM := filepath.Join(dir, "not.pem")
	if err := os.WriteFile(notPEM, []byte("not a certificate"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"not verified by default", nil, NagiosOk, "HTTP OK"},
		{"unknown authority", []string{"--verify-cert"}, NagiosCritical, "certificate signed by unknown authority"},
		{"trusted by --ca-cert", []string{"--verify-cert", "--ca-cert", caCert}, NagiosOk, "HTTP OK"},
		{"vhost matches the certificate", []string{"--verify-cert", "--ca-cert", caCert, "-H", "www.example.com"}, NagiosOk, "HTTP OK"},
		{"vhost does not match the certificate", []string{"--verify-cert", "--ca-cert", caCert, "-H", "www.example.org"}, NagiosCritical, "certificate is valid for"},
		{"--ca-cert without --verify-cert", []string{"--ca-cert", caCert}, NagiosUnknown, "HTTP UNKNOWN - --ca-cert requires --verify-cert"},
		{"--ca-cert not found", []string{"--verify-cert", "--ca-cert", filepath.Join(dir, "missing.pem")}, NagiosUnknown, "HTTP UNKNOWN - "},
		{"--ca-cert without certificates", []string{"--verify-cert", "--ca-cert", notPEM}, NagiosUnknown, "HTTP UNKNOWN - no certificates found in " + notPEM},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(append(serverArgs(t, srv), "-S"), tt.args...)...)
			assertResult(t, code, out, tt.wantCode, tt.wantOut)
		})
	}
}

// the perfdata has the thresholds and -t as max like check_http (#17)
func TestPerfData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{"default", nil, `time=[0-9.]+s;5\.000000;10\.000000;0\.000000;10\.000000 size=5B;;;0`},
		{"thresholds", []string{"-w", "1.5", "-c", "3", "-t", "4"}, `time=[0-9.]+s;1\.500000;3\.000000;0\.000000;4\.000000 size=5B;;;0`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(serverArgs(t, srv), tt.args...)...)
			if code != NagiosOk {
				t.Fatalf("exit code = %d, want %d\noutput:\n%s", code, NagiosOk, out)
			}
			re := regexp.MustCompile(`^HTTP OK: HTTP/1\.1 200 OK - 5 bytes in [0-9.]+ second response time \|` + tt.want + "\n$")
			if !re.MatchString(out) {
				t.Errorf("output %q does not match %s", out, re)
			}
		})
	}
}

func TestVerbose(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "1")
		_, _ = fmt.Fprint(w, "hello\n")
	}))
	defer srv.Close()

	code, stdout, stderr := runCheckWithStderr(t, append(serverArgs(t, srv), "-v", "-H", "www.example.com", "-k", "X-Foo: bar")...)
	// stdout must start with the status line so that Nagios can parse it (#13)
	if code != NagiosOk || !strings.HasPrefix(stdout, "HTTP OK: ") {
		t.Errorf("exit code = %d, stdout does not start with the status line\nstdout:\n%s", code, stdout)
	}
	if strings.Contains(stdout, "hello") {
		t.Errorf("stdout contains the response body\nstdout:\n%s", stdout)
	}
	for _, want := range []string{
		"> GET " + srv.URL + "/\n",
		"> Host: www.example.com\n",
		"> X-Foo: bar\n",
		"< HTTP/1.1 200 OK\n",
		"< X-Test: 1\n",
		"\nhello\n",
	} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr does not contain %q\nstderr:\n%s", want, stderr)
		}
	}
}

func TestInvalidArgs(t *testing.T) {
	t.Run("no host", func(t *testing.T) {
		code, out := runCheck(t)
		assertResult(t, code, out, NagiosUnknown, "HTTP UNKNOWN - -H or -I is required")
	})
	t.Run("unknown option", func(t *testing.T) {
		code, _ := runCheck(t, "-I", "127.0.0.1", "--no-such-option")
		if code != NagiosUnknown {
			t.Errorf("exit code = %d, want %d", code, NagiosUnknown)
		}
	})
}

func TestVersion(t *testing.T) {
	code, out := runCheck(t, "--version")
	assertResult(t, code, out, 0, "check_http_go: ")
}
