// SPDX-License-Identifier: Apache-2.0

package mask

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"net/url"
	"strings"
	"testing"
	"testing/iotest"
)

// Пересекающиеся значения: хвост длинного не выходит наружу при любом порядке.
func TestOverlapping(t *testing.T) {
	for i := 0; i < 50; i++ {
		vals := []string{"abcd", "abcd-LONGSECRET-xyz"}
		if i%2 == 1 {
			vals[0], vals[1] = vals[1], vals[0]
		}
		got := New(vals...).String("x abcd-LONGSECRET-xyz y abcd z", "***")
		if got != "x *** y *** z" {
			t.Fatal(got)
		}
	}
	// Значения, перекрывающиеся краями, — один участок.
	if got := New("abcdef", "defghi").String("--abcdefghi--", "#"); got != "--#--" {
		t.Fatal(got)
	}
}

func TestDerivedForms(t *testing.T) {
	const v = "SUPER/secret<&>\"value+1"
	s := New(v)
	cases := map[string]string{
		"plain":     v,
		"upper":     strings.ToUpper(v),
		"base64":    base64.StdEncoding.EncodeToString([]byte(v)),
		"base64url": base64.URLEncoding.EncodeToString([]byte(v)),
		"basic":     base64.StdEncoding.EncodeToString([]byte("user:" + v)),
		"basic2":    base64.StdEncoding.EncodeToString([]byte("us:" + v + ":tail")),
		"query":     url.QueryEscape(v),
		"path":      url.PathEscape(v),
		"html":      html.EscapeString(v),
	}
	j, _ := json.Marshal(v)
	cases["json"] = string(j)
	cases["json-slash"] = strings.ReplaceAll(string(j), "/", `\/`)
	for name, in := range cases {
		out := s.String("pre "+in+" post", "[secret]")
		if strings.Contains(out, in) || !strings.Contains(out, "[secret]") {
			t.Errorf("%s: %q", name, out)
		}
	}
	// Короткие значения не маскируются.
	if got := New("abc").String("abc", "*"); got != "abc" {
		t.Fatal(got)
	}
}

func TestFillKeepsLength(t *testing.T) {
	b := []byte("key=sk-real-secret;")
	New("sk-real-secret").Fill(b)
	if string(b) != "key=**************;" {
		t.Fatal(string(b))
	}
}

// Поток: значение на границе чтений маскируется, длина не меняется,
// не связанные данные отдаются сразу.
func TestReader(t *testing.T) {
	s := New("abcd", "cdXYZ123")
	in := "start abcdXYZ123 mid abcd end" + strings.Repeat("x", 100)
	for _, r := range []io.Reader{strings.NewReader(in), iotest.OneByteReader(strings.NewReader(in)), iotest.HalfReader(strings.NewReader(in))} {
		out, err := io.ReadAll(s.Reader(r))
		if err != nil {
			t.Fatal(err)
		}
		want := "start ********** mid **** end" + strings.Repeat("x", 100)
		if string(out) != want {
			t.Fatalf("%q", out)
		}
	}
	// Данные без начала значения не задерживаются.
	pr, pw := io.Pipe()
	mr := s.Reader(pr)
	go pw.Write([]byte("data: hello\n\n"))
	buf := make([]byte, 64)
	n, err := mr.Read(buf)
	if err != nil || !bytes.Equal(buf[:n], []byte("data: hello\n\n")) {
		t.Fatal(n, err, string(buf[:n]))
	}
	pw.Close()
}
