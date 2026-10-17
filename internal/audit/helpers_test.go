// SPDX-License-Identifier: AGPL-3.0-only

package audit

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"os"
	"testing"
)

func filepathRead(p string) ([]byte, error) { return os.ReadFile(p) }

func tamperTar(t *testing.T, archive []byte, name string, f func([]byte) []byte) []byte {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var out bytes.Buffer
	gw := gzip.NewWriter(&out)
	tw := tar.NewWriter(gw)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		b, _ := io.ReadAll(tr)
		if h.Name == name {
			b = f(b)
			h.Size = int64(len(b))
		}
		tw.WriteHeader(h)
		tw.Write(b)
	}
	tw.Close()
	gw.Close()
	return out.Bytes()
}
