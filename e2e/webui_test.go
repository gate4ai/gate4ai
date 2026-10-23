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
	"github.com/gate4ai/gate4ai/internal/front/cli"
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
		chromedp.WaitReady(`form[action="/logout"]`), // ссылка уводит в UI своей страницей
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

// `gate4ai ui` открывает ссылку входа через страницу-передатчик file:// (токен не
// попадает в argv): открытое так окно в настоящем браузере сразу входит в UI.
// Раньше вход терялся: межсайтовый 303 с file:// шёл на «/» без только что
// поставленной cookie SameSite=Strict, и страница просила вызвать `gate4ai ui` ещё раз.
func TestUIOpensLoggedInThroughHandoff(t *testing.T) {
	chrome, err := exec.LookPath("google-chrome")
	if err != nil {
		if chrome, err = exec.LookPath("chromium"); err != nil {
			t.Skip("needs Chrome")
		}
	}
	g := startGate(t, "--ui", "127.0.0.1:0")
	r := g.run("", "", "ui", "--print")
	login := strings.TrimSpace(r.out)
	if r.code != 0 || !strings.HasPrefix(login, "http://127.0.0.1:") {
		t.Fatalf("ui --print: %d %s", r.code, r.out)
	}
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	var opened string
	old := cli.OpenURL
	t.Cleanup(func() { cli.OpenURL = old })
	cli.OpenURL = func(u string) error { opened = u; return nil }
	if err := cli.OpenLoginURL(login); err != nil || !strings.HasPrefix(opened, "file://") {
		t.Skipf("no handoff page here: %q %v", opened, err)
	}

	actx, cancel := chromedp.NewExecAllocator(context.Background(), append(chromedp.DefaultExecAllocatorOptions[:], chromedp.ExecPath(chrome))...)
	defer cancel()
	ctx, cancel := chromedp.NewContext(actx)
	defer cancel()
	ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var loc string
	if err := chromedp.Run(ctx,
		chromedp.Navigate(opened),
		chromedp.WaitReady(`form[action="/logout"]`),
		chromedp.Location(&loc),
	); err != nil {
		t.Fatal("the opened window is not logged in:", err)
	}
	if strings.Contains(loc, "t=") {
		t.Fatal("token left in the address:", loc)
	}
}
