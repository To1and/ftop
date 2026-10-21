// Command protogen renders the real ftop board, driven by the demo fleet, to a
// self-contained HTML page at several terminal sizes.
//
// It draws through internal/ui itself — the same canvas, the same packing, the
// same btop meter and graph code — so a layout problem visible here is a layout
// problem in ftop, and nothing here can drift from what the terminal shows.
//
//	go run ./cmd/protogen            # writes out/board.html
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"ftop/internal/demo"
	"ftop/internal/sshx"
	"ftop/internal/ui"
)

func main() {
	out := flag.String("o", "out/board.html", "output file")
	termOut := flag.String("term", "out/term.html", "xterm.js replay of the same frames")
	sizes := flag.String("sizes", "200x50,240x60,160x45,120x40", "terminal sizes to render")
	warm := flag.Int("warm", 120, "samples of history to replay first")
	flag.Parse()

	for _, p := range []string{*out, *termOut} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	newApp := func() *ui.App {
		app := ui.New(ui.Options{Hosts: demo.Hosts(), GraphRows: 1, Interval: time.Second})
		src := demo.New()
		src.Warm(*warm, func(m sshx.SampleMsg) { app.Deliver(m) }, func(m sshx.ErrMsg) { app.Deliver(m) })
		app.Deliver(ui.DemoLedger(time.Now()))
		return app
	}
	app := newApp()

	type frame struct {
		Title string   `json:"title"`
		Cols  int      `json:"cols"`
		Rows  int      `json:"rows"`
		ANSI  string   `json:"ansi"`
		Dots  []ui.Dot `json:"dots,omitempty"`
	}
	var frames []frame
	var b strings.Builder
	b.WriteString(head)
	for _, sz := range strings.Split(*sizes, ",") {
		var w, h int
		if _, err := fmt.Sscanf(sz, "%dx%d", &w, &h); err != nil {
			fmt.Fprintln(os.Stderr, "protogen: bad size", sz)
			os.Exit(2)
		}
		title := fmt.Sprintf("board · %d×%d", w, h)
		fmt.Fprintf(&b, "<section id=\"b%d\"><h2>%s</h2><pre>%s</pre></section>\n", w, title, app.RenderHTML(w, h))
		ansi, dots := app.RenderFrame(w, h)
		frames = append(frames, frame{title, w, h, ansi, dots})
	}
	fmt.Fprintf(&b, "<section><h2>detail · dc-a · 200×50</h2><pre>%s</pre></section>\n", app.RenderDetailHTML("dc-a", 200, 50))
	ansi, dots := app.RenderDetailFrame("dc-a", 200, 50)
	frames = append(frames, frame{"detail · dc-a · 200×50", 200, 50, ansi, dots})
	fmt.Fprintf(&b, "<section><h2>detail · node-a · whole machine · 160×45</h2><pre>%s</pre></section>\n", app.RenderDetailHTML("node-a", 160, 45))
	ansi, dots = app.RenderDetailFrame("node-a", 160, 45)
	frames = append(frames, frame{"detail · node-a · whole machine · 160×45", 160, 45, ansi, dots})
	fmt.Fprintf(&b, "<section><h2>processes · dc-a · 200×40</h2><pre>%s</pre></section>\n", app.RenderProcHTML("dc-a", 200, 40))
	ansi, dots = app.RenderProcFrame("dc-a", 200, 40)
	frames = append(frames, frame{"processes · dc-a · 200×40", 200, 40, ansi, dots})
	help, hdots := app.RenderHelpFrame(200, 50)
	frames = append(frames, frame{"help · 200×50", 200, 50, help, hdots})
	set, sdots := app.RenderSettingsFrame(200, 50)
	frames = append(frames, frame{"settings · 200×50", 200, 50, set, sdots})

	// The xterm.js page replays the exact bytes View returns through a real
	// terminal emulator, which is the only faithful way to judge glyph widths:
	// a browser <pre> falls back to proportional fonts for braille and ■.
	js, _ := json.Marshal(frames)
	if err := os.WriteFile(*termOut, []byte(strings.Replace(termPage, "__FRAMES__", string(js), 1)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, []byte(b.String()), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("wrote", *out)
}

const head = `<!doctype html><meta charset="utf-8"><title>ftop board</title>
<style>
:root{color-scheme:dark}
body{margin:0;padding:24px;background:#0b0b0b;color:#ccc;font-family:-apple-system,system-ui,sans-serif}
h2{font-size:14px;font-weight:600;margin:0 0 8px;color:#ddd}
section{margin:0 0 36px}
pre{margin:0;display:inline-block;background:#000;padding:10px 12px;border-radius:6px;
font-family:"SF Mono",ui-monospace,Menlo,monospace;font-size:11px;line-height:1.2;white-space:pre}
</style>
`

const termPage = `<!doctype html><meta charset="utf-8"><title>ftop · xterm.js</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css">
<script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/@xterm/addon-webgl@0.18.0/lib/addon-webgl.min.js"></script>
<style>
body{margin:0;padding:20px;background:#0b0b0b;color:#ccc;font-family:-apple-system,system-ui,sans-serif}
h2{font-size:14px;font-weight:600;margin:0 0 8px}
section{margin:0 0 28px}
.t{display:inline-block;background:#000;padding:8px;border-radius:6px}
</style>
<div id="root"></div>
<script>
// Braille as Ghostty and kitty draw it, not as the font does: square dots on a
// 2x4 grid spread evenly over the whole cell (margins half a pitch), so dot
// rows tile across cells at one pitch however tall the cell is.
function drawDots(box, term, dots) {
  if (!dots.length) return;
  const d = term._core._renderService.dimensions.css.cell, cw = d.width, ch = d.height;
  const scr = box.querySelector('.xterm-screen');
  const dpr = window.devicePixelRatio || 1;
  const cv = document.createElement('canvas');
  cv.width = Math.round(scr.clientWidth * dpr); cv.height = Math.round(scr.clientHeight * dpr);
  cv.style.cssText = 'position:absolute;left:0;top:0;width:' + scr.clientWidth + 'px;height:' + scr.clientHeight + 'px;pointer-events:none';
  scr.style.position = 'relative';
  scr.appendChild(cv);
  const g = cv.getContext('2d');
  const W = cw * dpr, H = ch * dpr, s = Math.max(1, Math.floor(Math.min(W / 4, H / 8)));
  // bit -> [column, row] in btop's braille numbering
  const pos = [[0,0],[0,1],[0,2],[1,0],[1,1],[1,2],[0,3],[1,3]];
  for (const dt of dots) {
    g.fillStyle = dt.FG;
    for (let b = 0; b < 8; b++) {
      if (!(dt.Bits & (1 << b))) continue;
      const [c, r] = pos[b];
      const cx = dt.X * W + W * (c * 2 + 1) / 4, cy = dt.Y * H + H * (r * 2 + 1) / 8;
      g.fillRect(Math.round(cx - s / 2), Math.round(cy - s / 2), s, s);
    }
  }
}
const frames = __FRAMES__;
const root = document.getElementById('root');
const size = Number(new URLSearchParams(location.search).get('fs') || 11), lh = Number(new URLSearchParams(location.search).get('lh') || 1);
const only = new URLSearchParams(location.search).get('only');
frames.forEach((f, i) => {
  if (only !== null && String(i) !== only) return;
  const sec = document.createElement('section');
  sec.innerHTML = '<h2>' + f.title + '</h2>';
  const box = document.createElement('div'); box.className = 't'; sec.appendChild(box);
  root.appendChild(sec);
  const term = new Terminal({cols: f.cols, rows: f.rows, convertEol: true, fontSize: size,
    fontFamily: '"Liga SFMono Nerd Font", "LigaSFMono Nerd Font", "SF Mono", Menlo, monospace', lineHeight: lh,
    theme: {background: '#000000'}, allowProposedApi: true, customGlyphs: true});
  term.open(box);
  try { term.loadAddon(new WebglAddon.WebglAddon()); } catch (e) { console.log('webgl', e); }
  term.write(f.ansi, () => drawDots(box, term, f.dots || []));
});
</script>
`
