package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestHeaderCommand(t *testing.T) {
	if os.Getenv("CHECK_HTTP_GO_HEADER_TEST") == "1" {
		for i, arg := range os.Args {
			if arg == "--" {
				os.Args = append(os.Args[:1], os.Args[i+1:]...)
				main()
				return
			}
		}
	}

	for _, tt := range []struct {
		header string
		value  string
		code   int
	}{
		{"X-Foo:bar", "bar", NagiosOk},
		{"X-Foo: bar", "bar", NagiosOk},
		{" X-Foo :\tbar \t", "bar", NagiosOk},
		{"X-Foo:one:two", "one:two", NagiosOk},
		{"X-Foo:", "", NagiosOk},
		{"X-Foo", "", NagiosUnknown},
	} {
		t.Run(tt.header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if values, ok := r.Header["X-Foo"]; !ok || len(values) != 1 || values[0] != tt.value {
					t.Errorf("X-Foo = %q, want [%q]", values, tt.value)
				}
			}))
			defer server.Close()
			host, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
			if err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestHeaderCommand$", "--", "-I", host, "-p", port, "-k", tt.header)
			cmd.Env = append(os.Environ(), "CHECK_HTTP_GO_HEADER_TEST=1")
			output, err := cmd.CombinedOutput()
			code := 0
			if err != nil {
				if exit, ok := err.(*exec.ExitError); ok {
					code = exit.ExitCode()
				} else {
					t.Fatal(err)
				}
			}
			if code != tt.code {
				t.Fatalf("exit code = %d, want %d; output: %s", code, tt.code, output)
			}
			if tt.code == NagiosUnknown && !strings.Contains(string(output), "HTTP UNKNOWN - invalid header:") {
				t.Fatalf("missing invalid header diagnostic: %s", output)
			}
		})
	}
}
