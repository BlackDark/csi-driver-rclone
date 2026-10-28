// Command fakeoauth serves package fakeoauth for the e2e tests.
package main

import (
	"flag"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/BlackDark/csi-driver-rclone/test/fakeoauth"
)

func main() {
	addr := flag.String("addr", ":8080", "listen address")
	ttl := flag.Duration("ttl", 20*time.Second, "access token lifetime")
	initial := flag.String("initial-refresh", "", "comma-separated refresh tokens accepted once each")
	flag.Parse()
	s := fakeoauth.New(*ttl, strings.Split(*initial, ",")...)
	srv := &http.Server{Addr: *addr, Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
