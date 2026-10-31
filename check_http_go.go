package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	flags "github.com/jessevdk/go-flags"
	"golang.org/x/net/http2"
	"io"
	"io/ioutil"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
)

// https://golang.org/pkg/net/http/
// https://godoc.org/github.com/jessevdk/go-flags
// https://qiita.com/t-mochizuki/items/4ffc478fedae7b776805

type Options struct {
	Verbose        bool     `short:"v" long:"verbose"    description:"Show verbose debug information to stderr"`
	Vhost          string   `short:"H" long:"vhost"      description:"Host header"`
	Ipaddr         string   `short:"I" long:"ipaddr"     description:"IP address"`
	Port           int      `short:"p" long:"port"       description:"TCP Port" default:"0"`
	Warn           float64  `short:"w" long:"warn"       description:"Warning time in second" default:"5.0"`
	Crit           float64  `short:"c" long:"crit"       description:"Critical time in second" default:"10.0"`
	Headers        []string `short:"k" long:"header"    description:"additional headers, acceptable multiple times"`
	Timeout        int      `short:"t" long:"timeout"    description:"Timeout in second" default:"10"`
	Uri            string   `short:"u" long:"uri"        description:"URI" default:"/"`
	Ssl            bool     `short:"S" long:"ssl"        description:"Enable TLS"`
	Expect         string   `short:"e" long:"expect"     description:"Expected status codes or strings in the status line (csv)" default:""`
	JsonKey        string   `long:"json-key"   description:"Dot separated JSON key, array elements by index (e.g. items.0.status)"`
	JsonValue      string   `long:"json-value" description:"Expected JSON value, non-string values as JSON (e.g. 1, true, null)"`
	Method         string   `short:"j" long:"method"     description:"HTTP METHOD (GET, HEAD, POST)" default:"GET"`
	UserAgent      string   `short:"A" long:"useragent"  description:"User-Agent header" default:"check_http_go"`
	ClientCertFile string   `short:"J" long:"client-cert" description:"Client Certificate File"`
	PrivateKeyFile string   `short:"K" long:"private-key" description:"Private Key File"`
	VerifyCert     bool     `long:"verify-cert" description:"Verify the server certificate and host name"`
	CACertFile     string   `long:"ca-cert" description:"CA certificate file (PEM) to verify the server certificate with, used with --verify-cert"`
	Version        bool     `long:"version" description:"Print version"`
}

const (
	NagiosOk       = 0
	NagiosWarning  = 1
	NagiosCritical = 2
	NagiosUnknown  = 3
)

// version is set at build time with -ldflags "-X main.version=..."
var version = "dev"

// getVersion falls back to the module version embedded by `go install ...@vX.Y.Z`
func getVersion() string {
	if version != "dev" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		return info.Main.Version
	}
	return version
}

// tlsServerName strips the port from the -H value (e.g. example.com:5000)
func tlsServerName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

func genTlsConfig(opts Options, serverName string) *tls.Config {
	conf := &tls.Config{
		// http.Transport uses the URL host (the -I value) unless ServerName is set.
		// It is sent as SNI unless it is an IP address and verified with --verify-cert.
		ServerName:         serverName,
		InsecureSkipVerify: !opts.VerifyCert,
	}

	if opts.CACertFile != "" {
		b, err := os.ReadFile(opts.CACertFile)
		if err != nil {
			unknown("%s", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			unknown("no certificates found in %s", opts.CACertFile)
		}
		conf.RootCAs = pool
	}

	if opts.ClientCertFile != "" && opts.PrivateKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, opts.PrivateKeyFile)
		if err != nil {
			unknown("%s", err)
		}
		conf.Certificates = []tls.Certificate{cert}
	}

	return conf
}

// printHeaders writes headers to stderr in a stable order for --verbose
func printHeaders(prefix string, h http.Header) {
	for _, k := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[k] {
			fmt.Fprintf(os.Stderr, "%s%s: %s\n", prefix, k, v)
		}
	}
}

// unknown prints the message as the plugin output and exits with UNKNOWN
func unknown(format string, a ...interface{}) {
	fmt.Printf("HTTP UNKNOWN - "+format+"\n", a...)
	os.Exit(NagiosUnknown)
}

// matchExpect reports whether the response matches one of the comma separated
// expectations. A 3-digit value is compared with the status code; any other
// value is searched for in the status line (e.g. "HTTP/1.1 200 OK") as check_http does.
func matchExpect(expect string, proto string, status string, code int) bool {
	statusLine := proto + " " + status
	for _, e := range strings.Split(expect, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if n, err := strconv.Atoi(e); err == nil && len(e) == 3 {
			if n == code {
				return true
			}
		} else if strings.Contains(statusLine, e) {
			return true
		}
	}
	return false
}

// parseJSON decodes the body keeping numbers as written (e.g. 1.50, big integers)
func parseJSON(b []byte) (interface{}, error) {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v interface{}
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return v, nil
}

// lookupJSON follows the dot separated key. Object members are looked up by
// name and array elements by index (e.g. "items.0.status").
func lookupJSON(v interface{}, key string) (interface{}, bool) {
	for _, k := range strings.Split(key, ".") {
		switch node := v.(type) {
		case map[string]interface{}:
			child, ok := node[k]
			if !ok {
				return nil, false
			}
			v = child
		case []interface{}:
			i, err := strconv.Atoi(k)
			if err != nil || i < 0 || i >= len(node) {
				return nil, false
			}
			v = node[i]
		default:
			return nil, false
		}
	}
	return v, true
}

// jsonValueString returns the value in the form given to --json-value: strings
// as they are and other values as JSON (e.g. 1, true, null, {"a":1})
func jsonValueString(v interface{}) string {
	if s, ok := v.(string); ok {
		return s
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	// keep <, > and & as they are instead of \u003c etc.
	enc.SetEscapeHTML(false)
	// values decoded by parseJSON can always be encoded
	_ = enc.Encode(v)
	return strings.TrimSuffix(b.String(), "\n")
}

func prettyPrintJSON(b []byte) ([]byte, error) {
	var out bytes.Buffer
	err := json.Indent(&out, b, "", "    ")
	return out.Bytes(), err
}

func main() {
	var opts Options
	var result_messages []string
	var host_header string
	var additional_out []byte
	scheme := "http"
	parser := flags.NewParser(&opts, flags.Default)
	_, err := parser.Parse()
	if err != nil {
		os.Exit(NagiosUnknown)
	}

	if opts.Version {
		fmt.Printf("check_http_go: %s\n", getVersion())
		os.Exit(0)
	}

	if opts.Ipaddr == "" && opts.Vhost != "" {
		opts.Ipaddr = opts.Vhost
	}
	if opts.Ipaddr == "" {
		unknown("-H or -I is required")
	}
	// IsSet is used instead of comparing with "" so that an empty --json-value can be checked
	jsonCheck := parser.FindOptionByLongName("json-key").IsSet()
	if jsonCheck != parser.FindOptionByLongName("json-value").IsSet() {
		unknown("--json-key and --json-value must be specified together")
	}
	if jsonCheck && opts.JsonKey == "" {
		unknown("--json-key must not be empty")
	}
	if opts.CACertFile != "" && !opts.VerifyCert {
		unknown("--ca-cert requires --verify-cert")
	}
	if opts.Warn > opts.Crit {
		unknown("warning threshold %.3fs must not be greater than critical threshold %.3fs", opts.Warn, opts.Crit)
	}
	host_header = opts.Ipaddr
	if opts.Vhost != "" {
		host_header = opts.Vhost
	}
	if opts.Ssl {
		scheme = "https"
	}
	if opts.Port == 0 {
		if opts.Ssl {
			opts.Port = 443
		} else {
			opts.Port = 80
		}
	}

	// https://golang.org/pkg/crypto/tls/#Config
	tr := &http.Transport{
		TLSClientConfig: genTlsConfig(opts, tlsServerName(host_header)),
	}

	// https://github.com/golang/go/issues/17051
	// https://qiita.com/catatsuy/items/ee4fc094c6b9c39ee08f
	if err := http2.ConfigureTransport(tr); err != nil {
		unknown("failed to configure h2 transport: %s", err)
	}

	c := &http.Client{
		Timeout: time.Duration(opts.Timeout) * time.Second,
		// https://jonathanmh.com/tracing-preventing-http-redirects-golang/
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		Transport: tr,
	}

	url_str := scheme + "://" + opts.Ipaddr + ":" + strconv.Itoa(opts.Port) + opts.Uri

	values := url.Values{}

	req, err := http.NewRequest(opts.Method, url_str, strings.NewReader(values.Encode()))
	if err != nil {
		unknown("%s", err)
	}

	req.Host = host_header
	req.Header.Set("User-Agent", opts.UserAgent)

	for _, header := range opts.Headers {
		hdr := strings.SplitN(header, ": ", 2)
		req.Header.Set(hdr[0], hdr[1])
	}

	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "> %s %s\n", req.Method, req.URL)
		fmt.Fprintf(os.Stderr, "> Host: %s\n", req.Host)
		printHeaders("> ", req.Header)
	}

	t1 := time.Now()

	resp, err := c.Do(req)
	if err != nil {
		fmt.Printf("HTTP CRITICAL - %s\n", err)
		os.Exit(NagiosCritical)
	}

	defer resp.Body.Close()
	buf, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		fmt.Printf("HTTP CRITICAL - %s\n", err)
		os.Exit(NagiosCritical)
	}

	t2 := time.Now()
	diff := t2.Sub(t1)

	size := len(buf)

	// stdout is reserved for the plugin output, whose first line is parsed by Nagios
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "< %s %s\n", resp.Proto, resp.Status)
		printHeaders("< ", resp.Header)
		fmt.Fprintf(os.Stderr, "\n%s", buf)
	}

	nagios_status := NagiosOk
	// fail records a reason and raises the status to at least the given level
	fail := func(status int, format string, a ...interface{}) {
		if status > nagios_status {
			nagios_status = status
		}
		result_messages = append(result_messages, fmt.Sprintf(format, a...))
	}

	if opts.Expect == "" {
		if resp.StatusCode >= 500 {
			fail(NagiosCritical, "Unexpected http status code: %d", resp.StatusCode)
		} else if resp.StatusCode >= 400 {
			fail(NagiosWarning, "Unexpected http status code: %d", resp.StatusCode)
		}
	} else if !matchExpect(opts.Expect, resp.Proto, resp.Status, resp.StatusCode) {
		// check_http returns CRITICAL when the status line does not match -e
		fail(NagiosCritical, "Unexpected http status code: %d", resp.StatusCode)
	}

	if jsonCheck {
		if d, err := parseJSON(buf); err != nil {
			fail(NagiosCritical, "response body is not valid JSON: %s", err)
		} else {
			if v, ok := lookupJSON(d, opts.JsonKey); !ok {
				fail(NagiosCritical, "`%s` is not found in the response body", opts.JsonKey)
			} else if actual := jsonValueString(v); actual != opts.JsonValue {
				fail(NagiosCritical, "`%s` is not `%s` (actual: `%s`)", opts.JsonKey, opts.JsonValue, actual)
			}
			if out, err := prettyPrintJSON(buf); err == nil {
				additional_out = out
			}
		}
	}

	if diff.Seconds() > opts.Crit {
		fail(NagiosCritical, "response time %.3fs exceeded critical threshold %.3fs", diff.Seconds(), opts.Crit)
	} else if diff.Seconds() > opts.Warn {
		fail(NagiosWarning, "response time %.3fs exceeded warning threshold %.3fs", diff.Seconds(), opts.Warn)
	}

	result_str := "OK"
	if nagios_status == NagiosWarning {
		result_str = "WARNING"
	} else if nagios_status == NagiosCritical {
		result_str = "CRITICAL"
	}
	fmt.Printf("HTTP %s: %s %s - %d bytes in %.3f second response time |time=%.6fs;;;%.6f size=%dB;;;0\n", result_str, resp.Proto, resp.Status, size, diff.Seconds(), diff.Seconds(), 0.0, size)
	for _, m := range result_messages {
		fmt.Println(m)
	}
	if len(additional_out) > 0 {
		fmt.Printf("\n%s", additional_out)
	}
	os.Exit(nagios_status)
}
