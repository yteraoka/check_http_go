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
  -v, --verbose      Show verbose debug information to stderr
  -H, --vhost=       Host header
  -I, --ipaddr=      IP address
  -p, --port=        TCP Port (default: 0)
  -w, --warn=        Warning time in second (default: 5.0)
  -c, --crit=        Critical time in second (default: 10.0)
  -k, --header=      additional headers, acceptable multiple times
  -t, --timeout=     Timeout in second (default: 10)
  -u, --uri=         URI (default: /)
  -S, --ssl          Enable TLS
  -e, --expect=      Expected status codes or strings in the status line (csv)
      --json-key=    Dot separated JSON key, array elements by index (e.g.
                     items.0.status)
      --json-value=  Expected JSON value, non-string values as JSON (e.g. 1,
                     true, null)
  -j, --method=      HTTP METHOD (GET, HEAD, POST) (default: GET)
  -A, --useragent=   User-Agent header (default: check_http_go)
  -J, --client-cert= Client Certificate File
  -K, --private-key= Private Key File
      --version      Print version

Help Options:
  -h, --help         Show this help message
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

### Differences from `check_http`

- `-e`/`--expect`: a 3-digit value (e.g. `200`) is compared with the status code exactly, while other values (e.g. `HTTP/1.1 200`) are searched for in the status line as `check_http` does. Spaces around commas are ignored. If no value matches, the result is CRITICAL.
- When multiple checks fail (status code, JSON value and response time), all reasons are printed and the most severe status is returned.
- If `-w` is greater than `-c`, the result is UNKNOWN.

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
