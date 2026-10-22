// SPDX-License-Identifier: AGPL-3.0-only

package e2e

import (
	"bytes"
	"context"
	"encoding/base64"
	"image/png"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/makiuchi-d/gozxing"
	"github.com/makiuchi-d/gozxing/qrcode"
)

// Вход с телефона в настоящем браузере: «Settings → Internet access → Show the link and the QR code»
// показывает картинку, которая загрузилась, и QR в ней — та же ссылка, что на странице.
func TestRemoteLoginQR(t *testing.T) {
	t.Parallel()
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	g := startGate(t, "--ui", "127.0.0.1:0", "--public", "127.0.0.1:0", "--public-url", "https://phone.example")
	r := g.run("", "", "ui", "--print")
	login := strings.TrimSpace(r.out)
	if r.code != 0 || !strings.HasPrefix(login, "http://127.0.0.1:") {
		t.Fatalf("ui --print: %d %s", r.code, r.out)
	}
	base := login[:strings.Index(login, "/login")]

	actx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, time.Minute)
	defer cancel()

	var src, link, shot string
	var width int
	err = chromedp.Run(ctx,
		chromedp.Navigate(login),
		chromedp.Navigate(base+"/lang?l=en&back=/settings/internet#remote"),
		chromedp.Click(`//button[text()="Show the link and the QR code"]`),
		chromedp.WaitVisible(`#rlink`),
		chromedp.Text(`#rlink`, &link),
		chromedp.Poll(`(() => { const i = document.querySelector('img[alt="QR code of the sign-in link"]'); return i && i.complete })()`, nil),
		chromedp.Evaluate(`document.querySelector('img[alt="QR code of the sign-in link"]').getAttribute('src')`, &src),
		chromedp.Evaluate(`document.querySelector('img[alt="QR code of the sign-in link"]').naturalWidth`, &width),
		// то, что показал браузер, а не то, что прислал сервер
		chromedp.Evaluate(`(() => {
			const i = document.querySelector('img[alt="QR code of the sign-in link"]'), c = document.createElement('canvas');
			c.width = i.naturalWidth; c.height = i.naturalHeight;
			c.getContext('2d').drawImage(i, 0, 0);
			return c.toDataURL('image/png');
		})()`, &shot),
	)
	if err != nil {
		t.Fatal(err)
	}
	link = strings.TrimSpace(link)
	if !strings.HasPrefix(link, "https://phone.example/") {
		t.Fatalf("link: %q", link)
	}
	if src == "" || strings.Contains(src, "ZgotmplZ") || width == 0 {
		t.Fatalf("QR image not loaded: src=%.60q naturalWidth=%d", src, width)
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(shot, "data:image/png;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	bmp, err := gozxing.NewBinaryBitmapFromImage(img)
	if err != nil {
		t.Fatal(err)
	}
	res, err := qrcode.NewQRCodeReader().Decode(bmp, nil)
	if err != nil {
		t.Fatalf("QR not decoded: %v", err)
	}
	if res.GetText() != link {
		t.Fatalf("QR %q, link on the page %q", res.GetText(), link)
	}
}
