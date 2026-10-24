// SPDX-License-Identifier: AGPL-3.0-only

// testsite — тестовый сайт gate4ai руками: go run ./e2e/testsite/cmd/testsite
// (по умолчанию http://127.0.0.1:8099). Учётка — на странице /login.
package main

import (
	"flag"
	"fmt"
	"net/http"
	"os"

	"github.com/gate4ai/gate4ai/e2e/testsite"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:8099", "address to listen on")
	flag.Parse()
	fmt.Printf("testsite: http://%s/ (sign in: %s / %s)\n", *listen, testsite.User, testsite.Password)
	if err := http.ListenAndServe(*listen, testsite.New().Handler()); err != nil {
		fmt.Fprintln(os.Stderr, "testsite:", err)
		os.Exit(1)
	}
}
