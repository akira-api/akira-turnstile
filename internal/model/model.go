package model

const TurnstileBinding = "__solverTurnstileToken"
const PausedBufSize = 128
const FakePage = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title></title>
</head>
<body>
    <div class="turnstile"></div>
    <script>
        window.onloadTurnstileCallback = function () {
            turnstile.render('.turnstile', {
                sitekey: '<site-key>',
                callback: function (token) {
                    var c = document.createElement('input');
                    c.type = 'hidden';
                    c.name = 'cf-response';
                    c.value = token;
                    document.body.appendChild(c);
                    if (typeof window.` + TurnstileBinding + ` === 'function') {
                        window.` + TurnstileBinding + `(token);
                    }
                },
            });
        };
    </script>
    <script src="https://challenges.cloudflare.com/turnstile/v0/api.js?onload=onloadTurnstileCallback"></script>
</body>
</html>`

type SolveReq struct {
	URL     string `json:"url" binding:"required"`
	SiteKey string `json:"sitekey" binding:"required"`
}

type SolveResp struct {
	Token     string `json:"token"`
	BootMS    int64  `json:"boot_ms"`
	NavMS     int64  `json:"nav_ms"`
	DetectMS  int64  `json:"detect_ms"`
	HitCount  int    `json:"hit_count"`
	CFDelayMS int64  `json:"cf_delay_ms"`
	SolveMS   int64  `json:"solve_ms"`
}

type SolveResult struct {
	Token     string
	BootMS    int64
	NavMS     int64
	DetectMS  int64
	LastHitMS int64
	HitCount  int
	SolveMS   int64
	Err       error
}

type SolveUAMReq struct {
	URL string `json:"url" binding:"required"`
}

type SolveUAMResp struct {
	CFClearance string       `json:"cf_clearance"`
	UserAgent   string       `json:"user_agent"`
	Cookies     []CookieInfo `json:"cookies"`
	BootMS      int64        `json:"boot_ms"`
	SolveMS     int64        `json:"solve_ms"`
}

type CookieInfo struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}
