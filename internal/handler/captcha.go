package handler

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"
)

// captchaWidgetHTMLTmpl is the iframe page served to Chrome MV3 extensions.
//
// Chrome MV3 enforces script-src 'self' so the extension embeds this page in
// an <iframe>. The page lives on the server (a normal web origin) and is free
// to load any Prosopo bundle — official CDN or self-hosted.
//
// The inline script is absent: the HTML references /captcha/widget.js
// (same-origin) so script-src 'self' covers it without needing 'unsafe-inline'
// or a fragile hash. The bundle URL is passed as a query param on the script
// src so widget.js can read it via import.meta.url — no global injection needed.
//
// %s is replaced with the URL-encoded bundle URL.
const captchaWidgetHTMLTmpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<style>*{margin:0;padding:0;box-sizing:border-box}body{background:transparent}</style>
</head>
<body>
<form><div id="c"></div></form>
<script type="module" src="/captcha/widget.js?bundle=%s"></script>
</body>
</html>`

// captchaWidgetJS is the module script loaded by the widget HTML page.
// It reads the bundle URL from its own import.meta.url query param so the
// same script works for both official CDN and self-hosted Prosopo deployments.
const captchaWidgetJS = `const self_ = new URL(import.meta.url);
const bundleURL = self_.searchParams.get('bundle')
  || 'https://js.prosopo.io/js/procaptcha.bundle.js';

const p = new URLSearchParams(location.search);
const siteKey = p.get('siteKey');
const theme = p.get('theme') || 'light';

// Inject the Prosopo bundle and wait for it to register window.procaptcha.
await new Promise((resolve, reject) => {
  const s = Object.assign(document.createElement('script'), {
    type: 'module',
    src: bundleURL,
    onload: resolve,
    onerror: reject,
  });
  document.head.appendChild(s);
});

// captchaType must match the site key's configuration in the Prosopo dashboard
// (frictionless | pow | image). Passed via VITE_PROSOPO_CAPTCHA_TYPE on the
// frontend; omit to use Prosopo's default.
const captchaType = p.get('captchaType');

const opts = {
  siteKey,
  theme,
  callback(token) {
    parent.postMessage({ type: 'procaptcha-token', token }, '*');
  },
};
if (captchaType) opts.captchaType = captchaType;

window.procaptcha.render(document.getElementById('c'), opts);

// Notify the parent frame of the widget's actual height for iframe resizing.
new ResizeObserver(() => {
  parent.postMessage({ type: 'procaptcha-resize', height: document.body.scrollHeight }, '*');
}).observe(document.body);
`

// CaptchaHandler serves the Procaptcha iframe widget.
type CaptchaHandler struct {
	// bundleURL is the full URL to procaptcha.bundle.js.
	// Official CDN: https://js.prosopo.io/js/procaptcha.bundle.js
	// Self-hosted:  https://your-prosopo.example.com/js/procaptcha.bundle.js
	bundleURL   string
	bundleOrigin string // scheme + host, used to build CSP
}

// NewCaptchaHandler creates a CaptchaHandler.
// bundleURL is the URL of the Prosopo JS bundle (official CDN or self-hosted).
func NewCaptchaHandler(bundleURL string) *CaptchaHandler {
	origin := "https://js.prosopo.io" // safe fallback
	if u, err := url.Parse(bundleURL); err == nil && u.Host != "" {
		origin = u.Scheme + "://" + u.Host
	}
	return &CaptchaHandler{bundleURL: bundleURL, bundleOrigin: origin}
}

// Widget serves the HTML shell for the Procaptcha iframe widget.
// The CSP script-src and connect-src are derived from the configured bundle
// URL so they work for both the official CDN and self-hosted deployments.
func (h *CaptchaHandler) Widget(c *gin.Context) {
	// Always allow the official Prosopo API domains for provider list / nodes.
	// For self-hosted deployments, additionally allow the custom bundle origin.
	connectSrc := "https://*.prosopo.io wss://*.prosopo.io https://provider-list.prosopo.io"
	scriptSrc := "'self' " + h.bundleOrigin
	if h.bundleOrigin != "https://js.prosopo.io" {
		connectSrc += " " + h.bundleOrigin
	}

	c.Header("Content-Security-Policy",
		"default-src 'none'; "+
			"script-src "+scriptSrc+"; "+
			"worker-src blob:; "+
			"style-src 'unsafe-inline'; "+
			"connect-src "+connectSrc+"; "+
			"img-src https: data:; "+
			"frame-ancestors http: https: chrome-extension:")

	html := fmt.Sprintf(captchaWidgetHTMLTmpl, url.QueryEscape(h.bundleURL))
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

// WidgetJS serves the module script for the Procaptcha widget page.
func (h *CaptchaHandler) WidgetJS(c *gin.Context) {
	c.Data(http.StatusOK, "text/javascript; charset=utf-8", []byte(captchaWidgetJS))
}
