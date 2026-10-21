// SPDX-License-Identifier: AGPL-3.0-only

package store

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// Сборка не из магазина по адресу: тот же путь, что у файла (подпись, --dev).
func TestPrepareFromURL(t *testing.T) {
	dir := t.TempDir()
	pkg := pack(t, "1", "api.bank.example", false, "")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bank-1.g4c" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(pkg)
	}))
	defer srv.Close()
	o := Options{Dir: filepath.Join(dir, "connectors")}
	ctx := context.Background()
	if _, err := o.Prepare(ctx, srv.URL+"/bank-1.g4c", false); err == nil || !strings.Contains(err.Error(), "--dev") {
		t.Fatal("unsigned package by URL without --dev:", err)
	}
	p, err := o.Prepare(ctx, srv.URL+"/bank-1.g4c", true)
	if err != nil || p.Connector != "bank" || p.Trust != TrustLocal || p.Source != srv.URL+"/bank-1.g4c" {
		t.Fatal(p, err)
	}
	if _, err := o.Prepare(ctx, srv.URL+"/missing.g4c", true); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatal("missing package:", err)
	}
}
