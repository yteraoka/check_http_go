check_http_go
=============

Nagios plugin to check HTTP written in go. This program can check value of JSON in HTTP response body.

Install
-------

Download a binary for your platform from [Releases](https://github.com/yteraoka/check_http_go/releases).

Or install with Go 1.26 or later:

```
go install github.com/yteraoka/check_http_go@latest
```

Or build from source:

```
git clone https://github.com/yteraoka/check_http_go.git
cd check_http_go
go build
```

Usage
-----

`check_http_go` is almost compatible with the original `check_http`.

```
Usage:
  check_http_go [OPTIONS]

Application Options:
  -v, --verbose                        Show verbose debug information to stderr
  -H, --vhost=                         Host header
  -I, --ipaddr=                        IP address
  -p, --port=                          TCP Port (default: 0)
  -w, --warn=                          Warning time in second (default: 5.0)
  -c, --crit=                          Critical time in second (default: 10.0)
  -k, --header=                        additional headers, acceptable multiple
                                       times
  -t, --timeout=                       Timeout in second (default: 10)
  -u, --uri=                           URI (default: /)
  -S, --ssl                            Enable TLS
  -4, --use-ipv4                       Use IPv4 connection
  -6, --use-ipv6                       Use IPv6 connection
  -s, --string=                        String to expect in the content
  -r, --regex=                         Regular expression (RE2 syntax) to
                                       search for in the content
  -R, --eregi=                         Case-insensitive regular expression (RE2
                                       syntax) to search for in the content
      --invert-regex                   Return CRITICAL if the regular
                                       expression is found, OK if not
  -m, --pagesize=MIN[:MAX]             Minimum page size required (bytes) :
                                       Maximum page size required (bytes)
  -f, --onredirect=                    How to handle a redirect: ok, warning,
                                       critical or follow (default: ok)
      --max-redirs=                    Maximal number of redirects with -f
                                       follow (default: 15)
  -e, --expect=                        Expected status codes or strings in the
                                       status line (csv)
      --json-key=                      Dot separated JSON key, array elements
                                       by index (e.g. items.0.status)
      --json-value=                    Expected JSON value, non-string values
                                       as JSON (e.g. 1, true, null)
  -j, --method=                        HTTP METHOD (GET, HEAD, POST), POST by
                                       default with -P (default: GET)
  -P, --post=                          URL-encoded HTTP POST data
  -T, --content-type=                  Content-Type header with -P (default:
                                       application/x-www-form-urlencoded)
  -A, --useragent=                     User-Agent header (default:
                                       check_http_go)
  -a, --authorization=AUTH_PAIR        Username:password on sites with basic
                                       authentication
  -J, --client-cert=                   Client Certificate File
  -K, --private-key=                   Private Key File
      --verify-cert                    Verify the server certificate and host
                                       name
  -C, --certificate=WARN[,CRIT]        Minimum number of days a certificate has
                                       to be valid, implies -S. The URL is not
                                       checked unless
                                       --continue-after-certificate is given
      --continue-after-certificate     Check the URL as well with -C
      --ca-cert=                       CA certificate file (PEM) to verify the
                                       server certificate with, used with
                                       --verify-cert
      --version                        Print version

Help Options:
  -h, --help                           Show this help message
```

If target endpoint returns below:

```json
{
  "xxx": {
    "status": "ok"
  }
}
```

use `--json-key` and `--json-value`

```
check_http_go ... --json-key=xxx.status --json-value=ok
```

Array elements are referred to by index, including a top-level array.

```
# {"items": [{"status": "ok"}, {"status": "ng"}]}
check_http_go ... --json-key=items.1.status --json-value=ng
```

Strings are compared as they are. Other values are compared with their JSON representation as written in the response body: numbers as written (e.g. `1`, `1.50`), `true`, `false`, `null`, and compact JSON with sorted keys for objects and arrays (e.g. `{"status":"ok"}`).

The result is CRITICAL when the value does not match, when the key is not found, or when the response body is not valid JSON. `--json-key` and `--json-value` must be specified together; `--json-value=` checks for an empty string.

### Host and port

`-I` and `-H` accept an IPv6 address with or without brackets (e.g. `-I ::1`, `-I [::1]`). An IPv6 address is put in brackets in the Host header.

When `-I` is not given, the host in `-H` is connected to. A port in `-H` (e.g. `-H example.com:5000`, `-H [::1]:5000`) is sent in the Host header and used to connect to unless `-p` is given, as `check_http` does.

`-u` is prefixed with `/` if it does not start with one (e.g. `-u health` requests `/health`).

`-4` and `-6` force connecting over IPv4 or IPv6.

### Content checks

```
check_http_go ... -s 'Example Domain'          # the body contains the string
check_http_go ... -r '^status: (ok|fine)$'     # the body matches the regular expression
check_http_go ... -R 'error' --invert-regex    # the body does not match, case-insensitive
check_http_go ... -m 100:10000                 # the body is 100 to 10000 bytes
```

`-s`, `-r` and `-R` fail with CRITICAL. The regular expressions are in the [RE2 syntax](https://github.com/google/re2/wiki/Syntax), and `^` and `$` match at line boundaries. `-m MIN[:MAX]` fails with WARNING as `check_http` does.

### Requests

```
check_http_go ... -P 'name=value'                          # POST with application/x-www-form-urlencoded
check_http_go ... -P '{"name":"value"}' -T application/json
check_http_go ... -a 'user:password'                       # basic authentication
```

`-P` sends POST unless `-j` is given. The `Authorization` header is masked in the `-v` output.

### Redirects

`-f` sets how to handle a redirect (301, 302, 303, 307 and 308 with a `Location` header):

- `ok` (default): OK with the redirect response.
- `warning` / `critical`: WARNING or CRITICAL with the redirect response. Skipped when `-e` is given, as `check_http` does.
- `follow`: follow redirects and check the last response. WARNING if more than `--max-redirs` (15 by default) redirects are needed.

### Output

The first line has the status, the reasons of a failure and the perfdata in the same format as `check_http`.
`|` and line breaks in the reasons are escaped as `¦`, `\r` and `\n` to keep the first line and the perfdata intact.

```
HTTP OK: HTTP/1.1 200 OK - 24 bytes in 0.011 second response time |time=0.011279s;5.000000;10.000000;0.000000;10.000000 size=24B;;;0
HTTP WARNING: HTTP/1.1 404 Not Found - Unexpected http status code: 404 - 460 bytes in 0.002 second response time |time=0.001862s;5.000000;10.000000;0.000000;10.000000 size=460B;;;0
```

The perfdata of `time` has `-w`, `-c`, 0 and `-t` as the warning, critical, min and max values.

When the JSON check fails, the response body is pretty-printed after the first line to help find out why.

```
HTTP CRITICAL: HTTP/1.1 200 OK - `xxx.status` is not `ok` (actual: `ng`) - 24 bytes in 0.002 second response time |time=0.001960s;5.000000;10.000000;0.000000;10.000000 size=24B;;;0

{
    "xxx": {
        "status": "ng"
    }
}
```

### TLS

With `-S`, the host name given by `-H` (without the port) is sent as SNI, so the right certificate is selected even when connecting to an IP address with `-I`.

The server certificate is not verified by default, as `check_http` does. Use `--verify-cert` to verify the certificate chain against the system CA certificates and the host name (`-H`, or `-I` if `-H` is not given). Use `--ca-cert` with it to verify against your own CA. Verification failures are CRITICAL.

```
check_http_go -S -I 192.0.2.1 -H www.example.com --verify-cert
check_http_go -S -H internal.example.com --verify-cert --ca-cert=/etc/ssl/private-ca.pem
```

`-C WARN[,CRIT]` checks the days left until the certificate expires: WARNING within `WARN` days, CRITICAL within `CRIT` days or when expired. It implies `-S` and, as `check_http` does, checks only the certificate unless `--continue-after-certificate` is given.

```
$ check_http_go -H example.com -C 30,14
SSL OK - Certificate 'example.com' will expire on 2026-12-25 22:56 +0000.
$ check_http_go -H example.com -C 30,14 --continue-after-certificate -u /health
```

### Differences from `check_http`

- `-e`/`--expect`: a 3-digit value (e.g. `200`) is compared with the status code exactly, while other values (e.g. `HTTP/1.1 200`) are searched for in the status line as `check_http` does. Spaces around commas are ignored. If no value matches, the result is CRITICAL.
- When multiple checks fail (status code, JSON value and response time), all reasons are printed on the first line separated by `; ` and the most severe status is returned.
- If `-w` is greater than `-c`, the result is UNKNOWN.
- `-S` does not take a TLS version. `--sni` is not needed because SNI is always sent (see TLS above).
- `--verify-cert` and `--ca-cert` are named after the same options of `check_curl`.
- `-r` and `-R` take a regular expression in the RE2 syntax instead of the POSIX extended syntax. `-l` is not supported; use `(?s)` in the expression to let `.` match a newline.
- `-f` does not support `sticky` and `stickyport`.
- The expiry date in the output of `-C` is in UTC.

Development
-----------

```
go test ./...
```

Releases are built by [GoReleaser](https://goreleaser.com/) on GitHub Actions when a `v*` tag is pushed.

```
git tag v0.3.0
git push origin v0.3.0
```
