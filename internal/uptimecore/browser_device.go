package uptimecore

import (
	"context"
	"fmt"

	"github.com/chromedp/cdproto/emulation"
	"github.com/chromedp/chromedp"
)

// A browser check runs at a device preset's screen size, or a custom size. The
// phone preset is a mobile browser: its user agent, touch and pixel ratio 3.

type deviceSpec struct {
	Label  string
	W, H   int
	Scale  float64
	Mobile bool
}

const phoneUserAgent = "Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Mobile Safari/537.36"

var browserDevices = map[string]deviceSpec{
	"desktop": {"Desktop", browserViewportW, browserViewportH, 1, false},
	"laptop":  {"Laptop", 1440, 900, 1, false},
	"tablet":  {"Tablet", 768, 1024, 1, false},
	"phone":   {"Phone", 390, 844, 3, true},
}

// NormalizeDevice checks the device, desktop when none is given.
func (o *BrowserOptions) NormalizeDevice() error {
	if o.Device == "" {
		o.Device = "desktop"
	}
	if o.Device == "custom" {
		if o.ViewportW < 320 || o.ViewportW > 3840 || o.ViewportH < 320 || o.ViewportH > 2160 {
			return fmt.Errorf("a custom size must be 320–3840 wide and 320–2160 high")
		}
		return nil
	}
	if _, ok := browserDevices[o.Device]; !ok {
		return fmt.Errorf("unknown device %q: use desktop, laptop, tablet, phone or custom", o.Device)
	}
	o.ViewportW, o.ViewportH = 0, 0
	return nil
}

func (o BrowserOptions) device() deviceSpec {
	if o.Device == "custom" {
		return deviceSpec{Label: "Custom", W: o.ViewportW, H: o.ViewportH, Scale: 1}
	}
	if d, ok := browserDevices[o.Device]; ok {
		return d
	}
	return browserDevices["desktop"]
}

func (d deviceSpec) emulate(ctx context.Context) error {
	opts := []chromedp.EmulateViewportOption{chromedp.EmulateScale(d.Scale)}
	if d.Mobile {
		opts = append(opts, chromedp.EmulateMobile, chromedp.EmulateTouch, chromedp.EmulatePortrait)
	}
	if err := chromedp.Do(ctx, chromedp.EmulateViewport(int64(d.W), int64(d.H), opts...)); err != nil {
		return err
	}
	if d.Mobile {
		_, err := chromedp.Call(ctx, emulation.SetUserAgentOverride, emulation.SetUserAgentOverrideParams{UserAgent: phoneUserAgent, Platform: "Linux armv81"})
		return err
	}
	return nil
}
