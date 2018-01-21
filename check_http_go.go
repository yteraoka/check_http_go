package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"github.com/icza/dyno"
	flags "github.com/jessevdk/go-flags"
	"io/ioutil"
	"log"
	"net/http"
	"golang.org/x/net/http2"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// https://golang.org/pkg/net/http/
// https://godoc.org/github.com/jessevdk/go-flags
// https://qiita.com/t-mochizuki/items/4ffc478fedae7b776805

type Options struct {
	Verbose   bool    `short:"v" long:"verbose"    description:"Show verbose debug information"`
	Vhost     string  `short:"H" long:"vhost"      description:"Host header"`
	Ipaddr    string  `short:"I" long:"ipaddr"     description:"IP address"`
	Port      int     `short:"p" long:"port"       description:"TCP Port" default:"0"`
	Warn      float64 `short:"w" long:"warn"       description:"Warning time in second" default:"5.0"`
	Crit      float64 `short:"c" long:"crit"       description:"Critical time in second" default:"10.0"`
	Timeout   int     `short:"t" long:"timeout"    description:"Timeout in second" default:"10"`
	Uri       string  `short:"u" long:"uri"        description:"URI" default:"/"`
	Ssl       bool    `short:"S" long:"ssl"        description:"Enable TLS"`
	Expect    string  `short:"e" long:"expect"     description:"Expected status codes (csv)" default:""`
	JsonKey   string  `long:"json-key"   description:"JSON key "`
	JsonValue string  `long:"json-value" description:"Expected json value"`
	Method    string  `short:"j" long:"method"     description:"HTTP METHOD (GET, HEAD, POST)" default:"GET"`
	UserAgent string  `short:"A" long:"useragent"  description:"User-Agent header" default:"check_http_go"`
	ClientCertFile string `short:"J" long:"client-cert" description:"Client Certificate File"`
	PrivateKeyFile string `short:"K" long:"private-key" description:"Private Key File"`
}

const (
	NagiosOk       = 0
	NagiosWarning  = 1
	NagiosCritical = 2
	NagiosUnknown  = 3
)

func genTlsConfig(opts Options) (*tls.Config) {
	conf := &tls.Config{}

	conf.InsecureSkipVerify = true

	if opts.ClientCertFile != "" && opts.PrivateKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(opts.ClientCertFile, opts.PrivateKeyFile)
		if err != nil {
			fmt.Printf("HTTP UNKNOWN - %s\n", err)
			os.Exit(NagiosUnknown)
		}
		conf.Certificates = []tls.Certificate{cert}
	}

	return conf
}

func prettyPrintJSON(b []byte) ([]byte, error) {
	var out bytes.Buffer
	err := json.Indent(&out, b, "", "    ")
	return out.Bytes(), err
}

func main() {
	var opts Options
	var result_message string
	var additional_out []byte
	scheme := "http"
	_, err := flags.Parse(&opts)
	if err != nil {
		os.Exit(NagiosUnknown)
	}
	if opts.Ipaddr == "" && opts.Vhost != "" {
		opts.Ipaddr = opts.Vhost
	}
	if opts.Ipaddr == "" {
		os.Exit(NagiosUnknown)
	}
	if opts.Port == 0 {
		if opts.Ssl {
			scheme = "https"
			opts.Port = 443
		} else {
			opts.Port = 80
		}
	}

	// https://golang.org/pkg/crypto/tls/#Config
	tr := &http.Transport{
		TLSClientConfig: genTlsConfig(opts),
	}

	// https://github.com/golang/go/issues/17051
	// https://qiita.com/catatsuy/items/ee4fc094c6b9c39ee08f
	if err := http2.ConfigureTransport(tr); err != nil {
		log.Fatalf("Failed to configure h2 transport: %s", err)
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
		fmt.Printf("HTTP UNKNOWN - %s\n", err)
		os.Exit(NagiosUnknown)
	}

	req.Header.Set("User-Agent", opts.UserAgent)

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

	status_text := strconv.Itoa(resp.StatusCode)
	size := len(buf)

	if opts.Verbose {
		fmt.Print(string(buf))
	}

	nagios_status := NagiosOk

	if opts.Expect == "" {
		if resp.StatusCode >= 500 {
			nagios_status = NagiosCritical
			result_message = fmt.Sprintf("Unexpected http status code: %d", resp.StatusCode)
		} else if resp.StatusCode >= 400 {
			nagios_status = NagiosWarning
			result_message = fmt.Sprintf("Unexpected http status code: %d", resp.StatusCode)
		}
	} else {
		nagios_status = NagiosWarning
		for _, expect := range strings.Split(opts.Expect, ",") {
			if status_text == expect {
				nagios_status = NagiosOk
			}
		}
		if nagios_status == NagiosWarning {
			result_message = fmt.Sprintf("Unexpected http status code: %d", resp.StatusCode)
		}
	}

	if opts.JsonKey != "" && opts.JsonValue != "" {
		// https://stackoverflow.com/questions/27689058/convert-string-to-interface
		t := strings.Split(opts.JsonKey, ".")
		s := make([]interface{}, len(t))
		for i, v := range t {
			s[i] = v
		}
		// https://reformatcode.com/code/json/taking-a-json-string-unmarshaling-it-into-a-mapstringinterface-editing-and-marshaling-it-into-a-byte-seems-more-complicated-then-it-should-be
		var d map[string]interface{}
		json.Unmarshal(buf, &d)
		// https://qiita.com/hnakamur/items/c3560a4b780487ef6065
		v, _ := dyno.Get(d, s...)
		if v != opts.JsonValue {
			nagios_status = NagiosCritical
			result_message = fmt.Sprintf("`%s` is not `%s`", opts.JsonKey, opts.JsonValue)
		}
		additional_out, err = prettyPrintJSON(buf)
	}

	if nagios_status == NagiosOk {
		if diff.Seconds() > opts.Crit {
			nagios_status = NagiosCritical
			result_message = fmt.Sprintf("response time %3.fs exceeded critical threshold %.3fs", diff.Seconds(), opts.Crit)
		} else if diff.Seconds() > opts.Warn {
			nagios_status = NagiosWarning
			result_message = fmt.Sprintf("response time %3.fs exceeded warning threshold %.3fs", diff.Seconds(), opts.Warn)
		}
	}

	result_str := "OK"
	if nagios_status == NagiosWarning {
		result_str = "WARNING"
	} else if nagios_status == NagiosCritical {
		result_str = "CRITICAL"
	}
	fmt.Printf("HTTP %s: %s %s - %d bytes in %.3f second response time |time=%.6fs;;;%.6f size=%dB;;;0\n", result_str, resp.Proto, resp.Status, size, diff.Seconds(), diff.Seconds(), 0.0, size)
	if result_message != "" {
		fmt.Println(result_message)
	}
	if len(additional_out) > 0 {
		fmt.Printf("\n%s", additional_out)
	}
	os.Exit(nagios_status)
}
