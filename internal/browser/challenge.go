package browser

import (
	"context"
	"encoding/json"
	"math"
	"math/rand/v2"
	"time"

	"github.com/chromedp/cdproto/input"
	cdpruntime "github.com/chromedp/cdproto/runtime"
	"github.com/chromedp/chromedp"

	"projek/internal/helpers"
	"projek/internal/logger"
)

func (w *worker) checkClick(ctx context.Context, jobID string) bool {
	w.mu.Lock()
	if w.closing || w.instance == nil {
		w.mu.Unlock()
		return false
	}
	w.mu.Unlock()

	var clicked bool
	_ = chromedp.Run(ctx,
		chromedp.ActionFunc(func(actx context.Context) error {
			ec, err := helpers.TargetExec(actx)
			if err != nil {
				return err
			}

			script := `(() => {
				const targets = [];
				const add = (el, type) => {
					if (!el || typeof el.getBoundingClientRect !== 'function') return;
					const r = el.getBoundingClientRect();
					if (r.width > 0 && r.height > 0) targets.push({ x: r.left, y: r.top, w: r.width, h: r.height, type });
				};
				document.querySelectorAll('[name="cf-turnstile-response"]').forEach(el => {
					const p = el.parentElement;
					if (!p) return;
					const r = p.getBoundingClientRect();
					if (r.width > 0 && r.width <= 330 && r.height > 0) add(p, 'resp');
					else if (r.width > 330 && r.height >= 45 && r.height <= 90) add(p, 'row');
				});
				document.querySelectorAll('iframe').forEach(el => {
					const s = String(el.src || '');
					if (s.includes('challenges.cloudflare.com')) add(el, 'ifr');
				});
				const divs = (strict) => {
					document.querySelectorAll('div').forEach(el => {
						try {
							const r = el.getBoundingClientRect();
							const s = window.getComputedStyle(el);
							if (r.width > 290 && r.width <= 310 && !el.querySelector('*') && (!strict || (s.margin === '0px' && s.padding === '0px')))
								targets.push({ x: r.left, y: r.top, w: r.width, h: r.height, type: strict ? 'box' : 'div' });
						} catch(e) {}
					});
				};
				divs(true);
				if (targets.length === 0) divs(false);
				const scan = (root) => {
					if (!root || typeof root.querySelectorAll !== 'function') return;
					root.querySelectorAll('*').forEach(el => {
						try {
							const tag = String(el.tagName || '').toLowerCase();
							const typ = String(el.getAttribute('type') || '').toLowerCase();
							const role = String(el.getAttribute('role') || '').toLowerCase();
							const txt = String(el.innerText || el.textContent || '').toLowerCase().trim();
							if ((tag === 'input' && typ === 'checkbox') || role === 'checkbox') add(el, 'chk');
							if ((tag === 'label' || role === 'button') && txt.includes('verify you are human')) add(el, 'lbl');
							const r = el.getBoundingClientRect();
							if (r.width >= 18 && r.width <= 40 && r.height >= 18 && r.height <= 40) {
								const s = window.getComputedStyle(el);
								if (s.cursor === 'pointer' || s.borderStyle !== 'none') targets.push({ x: r.left, y: r.top, w: r.width, h: r.height, type: 'sq' });
							}
							if (el.shadowRoot) scan(el.shadowRoot);
						} catch(e) {}
					});
				};
				scan(document);
				const vis = targets.filter(t => t.x > -20 && t.y > -20 && t.x < innerWidth && t.y < innerHeight);
				const t = vis.find(x => x.type === 'chk') || vis.find(x => x.type === 'sq') || vis.find(x => x.type === 'row') || vis.find(x => x.type === 'lbl') || vis.find(x => x.type === 'ifr') || vis.find(x => x.type === 'resp') || vis[0];
				if (!t) return null;
				const el = document.elementFromPoint(t.x + 5, t.y + 5);
				if (el && typeof el.scrollIntoView === 'function') el.scrollIntoView({ block: 'center', inline: 'center' });
				return { x: t.x, y: t.y, w: t.w, h: t.h, type: t.type };
			})()`

			var info struct {
				X, Y, W, H float64
				Type       string
			}
			res, exc, err := cdpruntime.Evaluate(script).WithReturnByValue(true).WithAwaitPromise(true).Do(ec)
			if err != nil || exc != nil || len(res.Value) == 0 || string(res.Value) == "null" {
				return nil
			}
			if err := json.Unmarshal(res.Value, &info); err != nil {
				return nil
			}

			tx := info.X + 30 + (rand.Float64() * 10)
			switch info.Type {
			case "chk", "sq":
				tx = info.X + (info.W / 2.0) + (rand.Float64() * 3) - 1.5
			case "row":
				tx = info.X + 24 + (rand.Float64() * 4) - 2
			}
			ty := info.Y + (info.H / 2.0) + (rand.Float64() * 4) - 2
			if info.Type == "sq" && info.H < 35 {
				ty += 6
			}

			logger.Debugf("job %s worker %d checkClick hit [%s]: {%.2f, %.2f} on %.0fx%.0f", jobID, w.id, info.Type, tx, ty, info.W, info.H)

			if err := w.moveCursor(actx, ec, tx, ty, info.W); err != nil {
				return err
			}
			if err := helpers.SleepCtx(actx, 100*time.Millisecond); err != nil {
				return err
			}
			if err := input.DispatchMouseEvent(input.MousePressed, tx, ty).WithButton(input.Left).WithButtons(1).WithClickCount(1).WithPointerType(input.Mouse).Do(ec); err != nil {
				return err
			}
			if err := helpers.SleepCtx(actx, time.Duration(90+rand.IntN(120))*time.Millisecond); err != nil {
				return err
			}
			if err := input.DispatchMouseEvent(input.MouseReleased, tx, ty).WithButton(input.Left).WithButtons(0).WithClickCount(1).WithPointerType(input.Mouse).Do(ec); err != nil {
				return err
			}
			clicked = true
			return nil
		}),
	)
	return clicked
}

func (w *worker) moveCursor(ctx, execCtx context.Context, tx, ty, tw float64) error {
	w.mu.Lock()
	sx, sy := w.cursorX, w.cursorY
	if !w.cursorSet {
		sx, sy = rand.Float64()*200, rand.Float64()*200
	}
	w.mu.Unlock()

	path := ghostPath(sx, sy, tx, ty, tw)
	for _, p := range path {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if err := input.DispatchMouseEvent(input.MouseMoved, p[0], p[1]).Do(execCtx); err != nil {
			return err
		}
		if err := helpers.SleepCtx(ctx, time.Duration(6+rand.IntN(18))*time.Millisecond); err != nil {
			return err
		}
	}
	w.mu.Lock()
	w.cursorX, w.cursorY = tx, ty
	w.cursorSet = true
	w.mu.Unlock()
	return nil
}

func ghostPath(sx, sy, ex, ey, tw float64) [][2]float64 {
	dx, dy := ex-sx, ey-sy
	dist := math.Hypot(dx, dy)
	if dist < 1 {
		return [][2]float64{{ex, ey}}
	}
	spread := clampF(dist, 2, 200)
	if tw <= 0 {
		tw = 100
	}
	steps := int(math.Ceil((math.Log2(math.Log2(dist/tw+1)*2+1) + rand.Float64()*25) * 3))
	if steps < 25 {
		steps = 25
	}
	if steps > 80 {
		steps = 80
	}
	cp1, cp2 := ghostAnchors(sx, sy, ex, ey, spread)
	pts := make([][2]float64, steps+1)
	for i := 0; i <= steps; i++ {
		t := float64(i) / float64(steps)
		pts[i] = cubicBez(sx, sy, cp1[0], cp1[1], cp2[0], cp2[1], ex, ey, t)
	}
	return pts
}

func ghostAnchors(sx, sy, ex, ey, spread float64) ([2]float64, [2]float64) {
	side := 1.0
	if rand.IntN(2) == 0 {
		side = -1
	}
	gen := func() [2]float64 {
		mt := rand.Float64()
		mx := sx + (ex-sx)*mt
		my := sy + (ey-sy)*mt
		dx, dy := ex-sx, ey-sy
		l := math.Hypot(dx, dy)
		if l == 0 {
			return [2]float64{mx, my}
		}
		nx := -dy / l * spread * side * rand.Float64()
		ny := dx / l * spread * side * rand.Float64()
		return [2]float64{maxF(0, mx+nx), maxF(0, my+ny)}
	}
	a, b := gen(), gen()
	if a[0] > b[0] {
		return b, a
	}
	return a, b
}

func cubicBez(p0x, p0y, p1x, p1y, p2x, p2y, p3x, p3y, t float64) [2]float64 {
	u := 1 - t
	uu, uuu := u*u, u*u*u
	tt, ttt := t*t, t*t*t
	return [2]float64{
		maxF(0, uuu*p0x+3*uu*t*p1x+3*u*tt*p2x+ttt*p3x),
		maxF(0, uuu*p0y+3*uu*t*p1y+3*u*tt*p2y+ttt*p3y),
	}
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
