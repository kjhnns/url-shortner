// Command shortcli is a tiny client for the gojoe.run URL shortener JSON API.
//
// Configuration (env):
//
//	GOJOE_BASE_URL   base URL of the shortener (default https://gojoe.run)
//	GOJOE_API_TOKEN  bearer token for the API == the app password (required).
//	                 There is no separate API token; the shortener authenticates
//	                 the API with the same APP_PASSWORD (passctl gojoe/app-password).
//
// Subcommands:
//
//	shortcli create <slug> <url>
//	shortcli list
//	shortcli get <slug>
//	shortcli update <slug> <url>
//	shortcli delete <slug>
//
// It depends only on the standard library.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	base := strings.TrimRight(getenv("GOJOE_BASE_URL", "https://gojoe.run"), "/")
	token := os.Getenv("GOJOE_API_TOKEN")
	if token == "" {
		fail("GOJOE_API_TOKEN is not set (it carries the app password)")
	}

	cmd := os.Args[1]
	args := os.Args[2:]
	switch cmd {
	case "create":
		need(args, 2, "create <slug> <url>")
		body, _ := json.Marshal(map[string]string{"slug": args[0], "target": args[1]})
		printLink(req(http.MethodPost, base+"/api/links", token, body))
	case "list":
		status, raw := request(http.MethodGet, base+"/api/links", token, nil)
		if status >= 300 {
			failResp(status, raw)
		}
		var links []link
		mustJSON(raw, &links)
		if len(links) == 0 {
			fmt.Println("(no links)")
			return
		}
		fmt.Printf("%-20s %-8s %s\n", "SLUG", "CLICKS", "TARGET")
		for _, l := range links {
			fmt.Printf("%-20s %-8d %s\n", l.Slug, l.Clicks, l.Target)
		}
	case "get":
		need(args, 1, "get <slug>")
		printLink(req(http.MethodGet, base+"/api/links/"+args[0], token, nil))
	case "update":
		need(args, 2, "update <slug> <url>")
		body, _ := json.Marshal(map[string]string{"target": args[1]})
		printLink(req(http.MethodPut, base+"/api/links/"+args[0], token, body))
	case "delete":
		need(args, 1, "delete <slug>")
		status, raw := request(http.MethodDelete, base+"/api/links/"+args[0], token, nil)
		if status >= 300 {
			failResp(status, raw)
		}
		fmt.Printf("deleted /%s\n", args[0])
	default:
		usage()
		os.Exit(2)
	}
}

type link struct {
	Slug      string `json:"slug"`
	Target    string `json:"target"`
	Clicks    int64  `json:"clicks"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// req issues a request, fails on a non-2xx status, and returns the raw body.
func req(method, url, token string, body []byte) []byte {
	status, raw := request(method, url, token, body)
	if status >= 300 {
		failResp(status, raw)
	}
	return raw
}

func request(method, url, token string, body []byte) (int, []byte) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	r, err := http.NewRequest(method, url, rdr)
	if err != nil {
		fail(err.Error())
	}
	r.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(r)
	if err != nil {
		fail(err.Error())
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, raw
}

func printLink(raw []byte) {
	var l link
	mustJSON(raw, &l)
	fmt.Printf("/%s -> %s (%d clicks)\n", l.Slug, l.Target, l.Clicks)
}

func mustJSON(raw []byte, v any) {
	if err := json.Unmarshal(raw, v); err != nil {
		fail(fmt.Sprintf("could not parse server response: %v\n%s", err, string(raw)))
	}
}

func need(args []string, n int, form string) {
	if len(args) < n {
		fail("usage: shortcli " + form)
	}
}

func failResp(status int, raw []byte) {
	msg := strings.TrimSpace(string(raw))
	var e struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error != "" {
		msg = e.Error
	}
	fail(fmt.Sprintf("server returned %d: %s", status, msg))
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

func usage() {
	fmt.Fprint(os.Stderr, `shortcli - client for the gojoe.run URL shortener API

Usage:
  shortcli create <slug> <url>
  shortcli list
  shortcli get <slug>
  shortcli update <slug> <url>
  shortcli delete <slug>

Env:
  GOJOE_BASE_URL   default https://gojoe.run
  GOJOE_API_TOKEN  required bearer token == the app password
`)
}
