package main

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
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
	cmd := exec.Command(os.Args[0], args...)
	// GORACE: skip the 1s sleep at exit when the test binary is built with -race
	cmd.Env = append(os.Environ(), runMainEnv+"=1", "GORACE=atexit_sleep_ms=0")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("failed to run check_http_go: %s", err)
		}
		return exitErr.ExitCode(), string(out)
	}
	return 0, string(out)
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
		// TODO: check_http returns CRITICAL in this case (#16)
		{"expect unmatched", []string{"-u", "/200", "-e", "201,204"}, NagiosWarning, "Unexpected http status code: 200"},
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
		got := <-reqs
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
		got := <-reqs
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

func TestJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"xxx":{"status":"ok"}}`)
	}))
	defer srv.Close()

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  string
	}{
		{"matched", []string{"--json-key", "xxx.status", "--json-value", "ok"}, NagiosOk, "HTTP OK"},
		{"unmatched", []string{"--json-key", "xxx.status", "--json-value", "ng"}, NagiosCritical, "`xxx.status` is not `ng`"},
		{"missing key", []string{"--json-key", "yyy.status", "--json-value", "ok"}, NagiosCritical, "`yyy.status` is not `ok`"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(serverArgs(t, srv), tt.args...)...)
			assertResult(t, code, out, tt.wantCode, tt.wantOut)
		})
	}
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
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out := runCheck(t, append(serverArgs(t, srv), tt.args...)...)
			assertResult(t, code, out, tt.wantCode, tt.wantOut)
		})
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

func TestPerfData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "hello")
	}))
	defer srv.Close()

	code, out := runCheck(t, serverArgs(t, srv)...)
	if code != NagiosOk {
		t.Fatalf("exit code = %d, want %d\noutput:\n%s", code, NagiosOk, out)
	}
	firstLine := strings.SplitN(out, "\n", 2)[0]
	re := regexp.MustCompile(`^HTTP OK: HTTP/1\.1 200 OK - 5 bytes in [0-9.]+ second response time \|time=[0-9.]+s;;;0\.000000 size=5B;;;0$`)
	if !re.MatchString(firstLine) {
		t.Errorf("first line %q does not match %s", firstLine, re)
	}
}

func TestInvalidArgs(t *testing.T) {
	t.Run("no host", func(t *testing.T) {
		code, _ := runCheck(t)
		if code != NagiosUnknown {
			t.Errorf("exit code = %d, want %d", code, NagiosUnknown)
		}
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
