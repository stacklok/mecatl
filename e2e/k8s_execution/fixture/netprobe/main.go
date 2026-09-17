//go:build kind_execution_e2e

package main

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", "", "HTTP listen address")
	target := flag.String("target", "", "TCP endpoint to probe")
	timeout := flag.Duration("timeout", 3*time.Second, "dial timeout")
	flag.Parse()
	if *listen != "" {
		srv := &http.Server{Addr: *listen, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }), ReadHeaderTimeout: 2 * time.Second}
		if err := srv.ListenAndServe(); err != nil {
			fmt.Fprintln(os.Stderr, "listen failed")
			os.Exit(1)
		}
	}
	if *target == "" {
		fmt.Fprintln(os.Stderr, "target is required")
		os.Exit(2)
	}
	conn, err := net.DialTimeout("tcp", *target, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect denied or unavailable")
		os.Exit(1)
	}
	_ = conn.Close()
	fmt.Println("connected")
}
