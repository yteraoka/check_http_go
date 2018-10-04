check_http_go
=============

Nagios plugin to check HTTP written in go. This program can check value of JSON in HTTP response body.

Usage
-----

`check_http_go` is almost compatible with the original `check_http`.

```
Usage:
  check_http_go [OPTIONS]

Application Options:
  -v, --verbose      Show verbose debug information
  -H, --vhost=       Host header
  -I, --ipaddr=      IP address
  -p, --port=        TCP Port (default: 0)
  -w, --warn=        Warning time in second (default: 5.0)
  -c, --crit=        Critical time in second (default: 10.0)
  -k, --header=      additional headers, acceptable multiple times
  -t, --timeout=     Timeout in second (default: 10)
  -u, --uri=         URI (default: /)
  -S, --ssl          Enable TLS
  -e, --expect=      Expected status codes (csv)
      --json-key=    JSON key
      --json-value=  Expected json value
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
