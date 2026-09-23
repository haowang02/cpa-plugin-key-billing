package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"cpa-key-billing/internal/catalogproxy"
)

func run() error {
	listen := flag.String("listen", "127.0.0.1:18318", "HTTP listen address")
	upstream := flag.String("upstream", "http://127.0.0.1:8317", "CPA HTTP(S) origin")
	flag.Parse()
	proxy, err := catalogproxy.New(*upstream)
	if err != nil {
		return err
	}
	return http.ListenAndServe(*listen, proxy)
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "model catalog proxy:", err)
		os.Exit(1)
	}
}
