package main

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	flags "github.com/jessevdk/go-flags"
)

type Options struct {
	Verbose        bool     `short:"v" long:"verbose"    description:"Show verbose debug information to stderr"`
	Vhost          string   `short:"H" long:"vhost"      description:"Host header"`
	IPAddr         string   `short:"I" long:"ipaddr"     description:"IP address"`
	Port           int      `short:"p" long:"port"       description:"TCP Port" default:"0"`
	Warn           float64  `short:"w" long:"warn"       description:"Warning time in second" default:"5.0"`
	Crit           float64  `short:"c" long:"crit"       description:"Critical time in second" default:"10.0"`
	Headers        []string `short:"k" long:"header"    description:"additional headers, acceptable multiple times"`
	Timeout        float64  `short:"t" long:"timeout"    description:"Timeout in second" default:"10"`
	URI            string   `short:"u" long:"uri"        description:"URI" default:"/"`
	SSL            bool     `short:"S" long:"ssl"        description:"Enable TLS"`
	String         string   `short:"s" long:"string"     description:"String to expect in the content"`
	Regex          string   `short:"r" long:"regex"      description:"Regular expression (RE2 syntax) to search for in the content"`
	EregI          string   `short:"R" long:"eregi"      description:"Case-insensitive regular expression (RE2 syntax) to search for in the content"`
	InvertRegex    bool     `long:"invert-regex" description:"Return CRITICAL if the regular expression is found, OK if not"`
	PageSize       string   `short:"m" long:"pagesize"   description:"Minimum page size required (bytes) : Maximum page size required (bytes)" value-name:"MIN[:MAX]"`
	Expect         string   `short:"e" long:"expect"     description:"Expected status codes or strings in the status line (csv)" default:""`
	JSONKey        string   `long:"json-key"   description:"Dot separated JSON key, array elements by index (e.g. items.0.status)"`
	JSONValue      string   `long:"json-value" description:"Expected JSON value, non-string values as JSON (e.g. 1, true, null)"`
	Method         string   `short:"j" long:"method"     description:"HTTP METHOD (GET, HEAD, POST), POST by default with -P" default:"GET"`
	Post           string   `short:"P" long:"post"       description:"URL-encoded HTTP POST data"`
	ContentType    string   `short:"T" long:"content-type" description:"Content-Type header with -P" default-mask:"application/x-www-form-urlencoded"`
	UserAgent      string   `short:"A" long:"useragent"  description:"User-Agent header" default:"check_http_go"`
	Authorization  string   `short:"a" long:"authorization" description:"Username:password on sites with basic authentication" value-name:"AUTH_PAIR"`
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

// errUsage is returned when go-flags has already printed the error or the help
var errUsage = errors.New("usage error")

// config is the validated options and the values derived from them
type config struct {
	Options
	jsonCheck bool
	// regex is -r or -R compiled, regexSource is the expression as given
	regex       *regexp.Regexp
	regexSource string
	// minSize and maxSize are from -m, maxSize is -1 if not given
	minSize    int
	maxSize    int
	scheme     string
	hostHeader string
}

// parseArgs parses and validates the command line arguments
func parseArgs(args []string) (*config, error) {
	cfg := &config{scheme: "http"}
	parser := flags.NewParser(&cfg.Options, flags.Default)
	if _, err := parser.ParseArgs(args); err != nil {
		return nil, errUsage
	}
	if cfg.Version {
		return cfg, nil
	}

	if cfg.IPAddr == "" && cfg.Vhost != "" {
		cfg.IPAddr = cfg.Vhost
		// -H may have a port (e.g. example.com:5000), which is used to connect to
		// unless -p is given, as check_http does
		if host, port, err := net.SplitHostPort(cfg.Vhost); err == nil {
			cfg.IPAddr = host
			if cfg.Port == 0 {
				if cfg.Port, err = strconv.Atoi(port); err != nil {
					return nil, fmt.Errorf("invalid port in -H: %s", cfg.Vhost)
				}
			}
		}
	}
	// accept an IPv6 address in brackets (e.g. [::1]) as well
	cfg.IPAddr = strings.TrimSuffix(strings.TrimPrefix(cfg.IPAddr, "["), "]")
	if cfg.IPAddr == "" {
		return nil, errors.New("-H or -I is required")
	}
	// IsSet is used instead of comparing with "" so that an empty --json-value can be checked
	cfg.jsonCheck = parser.FindOptionByLongName("json-key").IsSet()
	if cfg.jsonCheck != parser.FindOptionByLongName("json-value").IsSet() {
		return nil, errors.New("--json-key and --json-value must be specified together")
	}
	if cfg.jsonCheck && cfg.JSONKey == "" {
		return nil, errors.New("--json-key must not be empty")
	}
	if cfg.Regex != "" && cfg.EregI != "" {
		return nil, errors.New("-r and -R cannot be specified together")
	}
	if cfg.Regex != "" || cfg.EregI != "" {
		// ^ and $ match at line boundaries like REG_NEWLINE of check_http
		mode := "(?m)"
		cfg.regexSource = cfg.Regex
		if cfg.EregI != "" {
			mode = "(?mi)"
			cfg.regexSource = cfg.EregI
		}
		re, err := regexp.Compile(mode + cfg.regexSource)
		if err != nil {
			return nil, fmt.Errorf("invalid regular expression: %s", err)
		}
		cfg.regex = re
	} else if cfg.InvertRegex {
		return nil, errors.New("--invert-regex requires -r or -R")
	}
	minSize, maxSize, err := parsePageSize(cfg.PageSize)
	if err != nil {
		return nil, err
	}
	cfg.minSize, cfg.maxSize = minSize, maxSize
	if cfg.Post != "" {
		// IsSet is also true when the default value is used
		if method := parser.FindOptionByLongName("method"); !method.IsSet() || method.IsSetDefault() {
			cfg.Method = http.MethodPost
		}
		if cfg.ContentType == "" {
			cfg.ContentType = "application/x-www-form-urlencoded"
		}
	} else if cfg.ContentType != "" {
		return nil, errors.New("-T requires -P")
	}
	// the value is not printed as it has the password
	if cfg.Authorization != "" && !strings.Contains(cfg.Authorization, ":") {
		return nil, errors.New("-a must be in the form of username:password")
	}
	if cfg.CACertFile != "" && !cfg.VerifyCert {
		return nil, errors.New("--ca-cert requires --verify-cert")
	}
	if cfg.Warn > cfg.Crit {
		return nil, fmt.Errorf("warning threshold %.3fs must not be greater than critical threshold %.3fs", cfg.Warn, cfg.Crit)
	}

	cfg.hostHeader = hostHeader(cfg.IPAddr)
	if cfg.Vhost != "" {
		cfg.hostHeader = hostHeader(cfg.Vhost)
	}
	if cfg.SSL {
		cfg.scheme = "https"
	}
	if cfg.Port == 0 {
		if cfg.SSL {
			cfg.Port = 443
		} else {
			cfg.Port = 80
		}
	}
	return cfg, nil
}

// parsePageSize parses -m in the form of MIN[:MAX] and returns -1 as max if it is not given
func parsePageSize(s string) (int, int, error) {
	if s == "" {
		return 0, -1, nil
	}
	minStr, maxStr, hasMax := strings.Cut(s, ":")
	minSize, err := strconv.Atoi(minStr)
	if err != nil || minSize < 0 {
		return 0, 0, fmt.Errorf("invalid -m: %s", s)
	}
	if !hasMax {
		return minSize, -1, nil
	}
	maxSize, err := strconv.Atoi(maxStr)
	if err != nil || maxSize < minSize {
		return 0, 0, fmt.Errorf("invalid -m: %s", s)
	}
	return minSize, maxSize, nil
}

// tlsServerName strips the port from the -H value (e.g. example.com:5000)
func tlsServerName(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
}

// hostHeader puts an IPv6 address in brackets for the Host header (e.g. [::1])
func hostHeader(host string) string {
	if strings.Contains(host, ":") && net.ParseIP(host) != nil {
		return "[" + host + "]"
	}
	return host
}

// buildURL puts an IPv6 address in brackets and makes sure that the URI starts with "/"
func buildURL(scheme string, host string, port int, uri string) string {
	// the zone of an IPv6 address (e.g. fe80::1%eth0) must be escaped in a URL
	host = strings.Replace(host, "%", "%25", 1)
	if !strings.HasPrefix(uri, "/") {
		uri = "/" + uri
	}
	return scheme + "://" + net.JoinHostPort(host, strconv.Itoa(port)) + uri
}

func genTLSConfig(opts Options, serverName string) (*tls.Config, error) {
	conf := &tls.Config{
		// http.Transport uses the URL host (the -I value) unless ServerName is set.
		// It is sent as SNI unless it is an IP address and verified with --verify-cert.
		ServerName:         serverName,
		InsecureSkipVerify: !opts.VerifyCert,
	}

	if opts.CACertFile != "" {
		b, err := os.ReadFile(opts.CACertFile)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(b) {
			return nil, fmt.Errorf("no certificates found in %s", opts.CACertFile)
		}
		conf.RootCAs = pool
	}

	if opts.ClientCertFile != "" && opts.PrivateKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, opts.PrivateKeyFile)
		if err != nil {
			return nil, err
		}
		conf.Certificates = []tls.Certificate{cert}
	}

	return conf, nil
}

func newClient(cfg *config) (*http.Client, error) {
	tlsConfig, err := genTLSConfig(cfg.Options, tlsServerName(cfg.hostHeader))
	if err != nil {
		return nil, err
	}
	return &http.Client{
		Timeout: time.Duration(cfg.Timeout * float64(time.Second)),
		// a redirect is not followed and the 3xx response is checked
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
		// Proxy is not set, so the environment variables like HTTP_PROXY are not used
		Transport: &http.Transport{
			TLSClientConfig: tlsConfig,
			// HTTP/2 is not enabled by default when TLSClientConfig is set
			ForceAttemptHTTP2: true,
		},
	}, nil
}

func newRequest(cfg *config) (*http.Request, error) {
	var body io.Reader
	if cfg.Post != "" {
		body = strings.NewReader(cfg.Post)
	}
	req, err := http.NewRequest(cfg.Method, buildURL(cfg.scheme, cfg.IPAddr, cfg.Port, cfg.URI), body)
	if err != nil {
		return nil, err
	}
	if cfg.ContentType != "" {
		req.Header.Set("Content-Type", cfg.ContentType)
	}

	req.Host = cfg.hostHeader
	req.Header.Set("User-Agent", cfg.UserAgent)
	if cfg.Authorization != "" {
		user, password, _ := strings.Cut(cfg.Authorization, ":")
		req.SetBasicAuth(user, password)
	}

	for _, header := range cfg.Headers {
		hdr := strings.SplitN(header, ": ", 2)
		req.Header.Set(hdr[0], hdr[1])
	}

	return req, nil
}

// printHeaders writes headers in a stable order for --verbose
func printHeaders(w io.Writer, prefix string, h http.Header) {
	for _, k := range slices.Sorted(maps.Keys(h)) {
		for _, v := range h[k] {
			if k == "Authorization" || k == "Proxy-Authorization" {
				v = maskCredentials(v)
			}
			fmt.Fprintf(w, "%s%s: %s\n", prefix, k, v)
		}
	}
}

// maskCredentials keeps only the scheme of an Authorization header (e.g. "Basic ********")
func maskCredentials(v string) string {
	if scheme, _, ok := strings.Cut(v, " "); ok {
		return scheme + " ********"
	}
	return "********"
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
	// keep <, > and & as they are instead of < etc.
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

// reasonEscaper keeps a reason on the first line and out of the perfdata,
// as values from the response body (e.g. a JSON value) may contain them
var reasonEscaper = strings.NewReplacer("|", "¦", "\r", `\r`, "\n", `\n`)

// result is the status of the check and the reasons of the failures
type result struct {
	status  int
	reasons []string
	// longOutput is printed after the first line
	longOutput []byte
}

// fail records a reason and raises the status to at least the given level
func (r *result) fail(status int, format string, a ...interface{}) {
	if status > r.status {
		r.status = status
	}
	r.reasons = append(r.reasons, reasonEscaper.Replace(fmt.Sprintf(format, a...)))
}

func (r *result) checkStatus(cfg *config, resp *http.Response) {
	if cfg.Expect == "" {
		if resp.StatusCode >= 500 {
			r.fail(NagiosCritical, "Unexpected http status code: %d", resp.StatusCode)
		} else if resp.StatusCode >= 400 {
			r.fail(NagiosWarning, "Unexpected http status code: %d", resp.StatusCode)
		}
	} else if !matchExpect(cfg.Expect, resp.Proto, resp.Status, resp.StatusCode) {
		// check_http returns CRITICAL when the status line does not match -e
		r.fail(NagiosCritical, "Unexpected http status code: %d", resp.StatusCode)
	}
}

func (r *result) checkBody(cfg *config, body []byte) {
	if cfg.String != "" && !bytes.Contains(body, []byte(cfg.String)) {
		r.fail(NagiosCritical, "string `%s` not found in the response body", cfg.String)
	}
	// check_http returns WARNING when the page size is out of the range
	if len(body) < cfg.minSize {
		r.fail(NagiosWarning, "page size %d too small (minimum %d)", len(body), cfg.minSize)
	} else if cfg.maxSize >= 0 && len(body) > cfg.maxSize {
		r.fail(NagiosWarning, "page size %d too large (maximum %d)", len(body), cfg.maxSize)
	}
	if cfg.regex != nil {
		if found := cfg.regex.Match(body); found && cfg.InvertRegex {
			r.fail(NagiosCritical, "pattern `%s` found in the response body", cfg.regexSource)
		} else if !found && !cfg.InvertRegex {
			r.fail(NagiosCritical, "pattern `%s` not found in the response body", cfg.regexSource)
		}
	}
}

func (r *result) checkJSON(cfg *config, body []byte) {
	d, err := parseJSON(body)
	if err != nil {
		r.fail(NagiosCritical, "response body is not valid JSON: %s", err)
		return
	}
	if v, ok := lookupJSON(d, cfg.JSONKey); !ok {
		r.fail(NagiosCritical, "`%s` is not found in the response body", cfg.JSONKey)
	} else if actual := jsonValueString(v); actual != cfg.JSONValue {
		r.fail(NagiosCritical, "`%s` is not `%s` (actual: `%s`)", cfg.JSONKey, cfg.JSONValue, actual)
	} else {
		return
	}
	// the body can be large, so print it only to help find out why the check failed
	if out, err := prettyPrintJSON(body); err == nil {
		r.longOutput = out
	}
}

func (r *result) checkResponseTime(cfg *config, elapsed time.Duration) {
	if elapsed.Seconds() > cfg.Crit {
		r.fail(NagiosCritical, "response time %.3fs exceeded critical threshold %.3fs", elapsed.Seconds(), cfg.Crit)
	} else if elapsed.Seconds() > cfg.Warn {
		r.fail(NagiosWarning, "response time %.3fs exceeded warning threshold %.3fs", elapsed.Seconds(), cfg.Warn)
	}
}

func statusText(status int) string {
	switch status {
	case NagiosOk:
		return "OK"
	case NagiosWarning:
		return "WARNING"
	case NagiosCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// formatOutput returns the plugin output: the first line has the status, the
// reasons and the perfdata, followed by the long output if any
func formatOutput(cfg *config, resp *http.Response, size int, elapsed time.Duration, r *result) string {
	var out strings.Builder
	// the reasons are on the first line, which is often the only line shown in notifications
	reasons := ""
	if len(r.reasons) > 0 {
		reasons = strings.Join(r.reasons, "; ") + " - "
	}
	// same format as check_http: the perfdata of time has the thresholds, 0 as min and the timeout as max
	fmt.Fprintf(&out, "HTTP %s: %s %s - %s%d bytes in %.3f second response time |time=%.6fs;%.6f;%.6f;0.000000;%.6f size=%dB;;;0\n",
		statusText(r.status), resp.Proto, resp.Status, reasons, size, elapsed.Seconds(), elapsed.Seconds(), cfg.Warn, cfg.Crit, cfg.Timeout, size)
	if len(r.longOutput) > 0 {
		fmt.Fprintf(&out, "\n%s", r.longOutput)
	}
	return out.String()
}

// unknown writes the error as the plugin output and returns UNKNOWN
func unknown(w io.Writer, err error) int {
	fmt.Fprintf(w, "HTTP UNKNOWN - %s\n", err)
	return NagiosUnknown
}

// run performs the check with the command line arguments, writes the plugin
// output to stdout and the --verbose output to stderr, and returns the exit status.
// stdout is reserved for the plugin output, whose first line is parsed by Nagios.
func run(args []string, stdout io.Writer, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if errors.Is(err, errUsage) {
		return NagiosUnknown
	}
	if err != nil {
		return unknown(stdout, err)
	}
	if cfg.Version {
		fmt.Fprintf(stdout, "check_http_go: %s\n", getVersion())
		return NagiosOk
	}

	client, err := newClient(cfg)
	if err != nil {
		return unknown(stdout, err)
	}
	req, err := newRequest(cfg)
	if err != nil {
		return unknown(stdout, err)
	}

	if cfg.Verbose {
		fmt.Fprintf(stderr, "> %s %s\n", req.Method, req.URL)
		fmt.Fprintf(stderr, "> Host: %s\n", req.Host)
		printHeaders(stderr, "> ", req.Header)
	}

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(stdout, "HTTP CRITICAL - %s\n", err)
		return NagiosCritical
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintf(stdout, "HTTP CRITICAL - %s\n", err)
		return NagiosCritical
	}
	elapsed := time.Since(start)

	if cfg.Verbose {
		fmt.Fprintf(stderr, "< %s %s\n", resp.Proto, resp.Status)
		printHeaders(stderr, "< ", resp.Header)
		fmt.Fprintf(stderr, "\n%s", body)
	}

	r := &result{status: NagiosOk}
	r.checkStatus(cfg, resp)
	r.checkBody(cfg, body)
	if cfg.jsonCheck {
		r.checkJSON(cfg, body)
	}
	r.checkResponseTime(cfg, elapsed)

	fmt.Fprint(stdout, formatOutput(cfg, resp, len(body), elapsed, r))
	return r.status
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
