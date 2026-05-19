package model

/** SolveDirectReq is the direct Turnstile solver request. */
type SolveDirectReq struct {
	URL string `json:"url" binding:"required"`
}

/** SolveDirectResp is the direct Turnstile solver response. */
type SolveDirectResp struct {
	Cookies string `json:"cookies"`
	SolveMS int64  `json:"solve_ms"`
}

/** CookieInfo holds cookie data. */
type CookieInfo struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Domain string `json:"domain"`
	Path   string `json:"path"`
}
