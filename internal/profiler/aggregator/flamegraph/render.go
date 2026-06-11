// Copyright 2026 The HuaTuo Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package rendor

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"io"
	"math/rand"
)

type frame struct {
	Children       []frame
	Name           string
	SampleCount    int64
	Depth          int
	LeftPercent    float32
	SamplePercent  float32
}

type Style struct {
	ImageWidth    int
	FontFamily    string
	FontSize      int
	FontWidth     float32
	XMargin       float32
	FramesYOffset int
	FrameHeight   int
	FramePad      int
}

func (s Style) Color(name string) string {
	v := randomNamehash(name)

	r := 205 + int(50*v)
	g := 0 + int(230*v)
	b := 0 + int(55*v)
	return fmt.Sprintf("rgb(%d,%d,%d)", r, g, b)
}

var DefaultStyle = Style{
	ImageWidth:    1200,
	FontFamily:    "Verdana",
	FontSize:      12,
	FontWidth:     0.59,
	XMargin:       10,
	FramesYOffset: 12 * 3,
	FrameHeight:   16,
	FramePad:      1,
}

type renderer struct {
	style         Style
	out           io.Writer
	maxFrameDepth int
}

//go:embed svg.js
var javascript string

func newRenderer(style Style, out io.Writer, maxFrameDepth int) renderer {
	return renderer{
		style:         style,
		out:           out,
		maxFrameDepth: maxFrameDepth,
	}
}

func (r *renderer) Start() {
	topMargin := r.style.FontSize * 3
	bottomMargin := r.style.FontSize*2 + 10

	h := r.maxFrameDepth*r.style.FrameHeight + r.style.FramesYOffset + topMargin + bottomMargin
	w := r.style.ImageWidth

	fmt.Fprintln(r.out, `<?xml version="1.0" standalone="no"?>`)
	fmt.Fprintln(r.out, `<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">`)
	fmt.Fprintf(r.out, `<svg version="1.1" width="%d" height="%d" onload="init(evt)" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">`+"\n", w, h, w, h)

	d := `<defs >
	<linearGradient id="background" y1="0" y2="1" x1="0" x2="0" >
		<stop stop-color="#eeeeee" offset="5%" />
		<stop stop-color="#eeeeb0" offset="95%" />
	</linearGradient>
</defs>`
	fmt.Fprintln(r.out, d)

	d = `<style type="text/css">
	.func_g:hover { stroke:black; stroke-width:0.5; cursor:pointer; }
</style>`
	fmt.Fprintln(r.out, d)

	d = `<script type="text/ecmascript">
<![CDATA[`
	fmt.Fprintln(r.out, d)
	fmt.Fprintln(r.out, javascript)
	fmt.Fprintln(r.out, "]]>")
	fmt.Fprintln(r.out, "</script>")
	fmt.Fprintf(r.out, `<rect x="0.0" y="0" width="%d" height="%d" fill="url(#background)"/>`+"\n", w, h)
	fmt.Fprintf(r.out, `<text text-anchor="middle" x="%d" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)"  >Flame Graph</text>`+"\n", r.style.ImageWidth/2, r.style.FontSize*2, r.style.FontSize*3/2, r.style.FontFamily)
	fmt.Fprintf(r.out, `<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="details" > </text>`+"\n", r.style.XMargin, h-topMargin/2, r.style.FontSize, r.style.FontFamily)
	fmt.Fprintf(r.out, `<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="unzoom" onclick="unzoom()" style="opacity:0.0;cursor:pointer" >Reset Zoom</text>`+"\n", r.style.XMargin, r.style.FontSize*2, r.style.FontSize, r.style.FontFamily)
	fmt.Fprintf(r.out, `<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="search" onmouseover="searchover()" onmouseout="searchout()" onclick="search_prompt()" style="opacity:0.1;cursor:pointer" >Search</text>`+"\n", float32(r.style.ImageWidth)-r.style.XMargin-100, r.style.FontSize*2, r.style.FontSize, r.style.FontFamily)
	fmt.Fprintf(r.out, `<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="matched" > </text>`+"\n", float32(r.style.ImageWidth)-r.style.XMargin-100, h-bottomMargin/2, r.style.FontSize, r.style.FontFamily)
}

func (r *renderer) End() {
	fmt.Fprintln(r.out, `</svg>`)
}

func (r *renderer) DrawFrame(f frame) {
	queue := make([]frame, 0, 1000)
	queue = append(queue, f)

	for len(queue) > 0 {
		r.drawFrame(queue[0])
		queue = append(queue, queue[0].Children...)
		queue = queue[1:]
	}
}

func (r *renderer) drawFrame(f frame) {
	title := fmt.Sprintf("%s (%d samples, %.2f%%)", f.Name, f.SampleCount, f.SamplePercent)
	color := r.style.Color(f.Name)

	usableWidth := float32(r.style.ImageWidth) - r.style.XMargin*2

	w := f.SamplePercent / 100 * usableWidth
	h := r.style.FrameHeight
	x := r.style.XMargin + (f.LeftPercent / 100 * usableWidth)
	y := r.style.FramesYOffset + (r.maxFrameDepth-f.Depth-1)*r.style.FrameHeight

	fmt.Fprintf(r.out, `<g class="func_g" onmouseover="s('%s')" onmouseout="c()" onclick="zoom(this)">`+"\n", title)
	fmt.Fprintf(r.out, "  <title>%s</title>\n", title)
	fmt.Fprintf(r.out, `  <rect x="%.1f" y="%d" width="%.1f" height="%d" fill="%s" rx="2" ry="2"/>`+"\n",
		x, y, w, h, color)

	text := r.fitText(f.Name, w)

	fmt.Fprintf(r.out, `  <text text-anchor="" x="%.1f" y="%.1f" font-size="%d" font-family="%s" fill="rgb(0,0,0)">%s</text>`+"\n",
		x+3, 3+float32(y)+float32(h)/2, r.style.FontSize, r.style.FontFamily, text)

	fmt.Fprintf(r.out, "</g>\n")
}

func randomNamehash(n string) float32 {
	sum := int64(0)
	for _, r := range n {
		sum += int64(r)
	}

	return rand.New(rand.NewSource(sum)).Float32()
}

func (r *renderer) fitText(text string, width float32) string {
	avail := int(width / (float32(r.style.FontSize) * r.style.FontWidth))
	if avail < 3 {
		return ""
	}

	if len(text) < avail {
		return text
	}

	var buf bytes.Buffer
	_, _ = buf.WriteString(text[:avail-2])
	_, _ = buf.WriteString("..")
	return html.EscapeString(buf.String())
}

func Render(stacks []Stack, out io.Writer) {
	RenderStyle(stacks, out, DefaultStyle)
}

func RenderStyle(stacks []Stack, out io.Writer, style Style) {
	var proc processor
	for _, s := range stacks {
		proc.Process(s)
	}
	proc.Finalize()
	frames, maxDepth := proc.Result()

	render := newRenderer(style, out, maxDepth)
	render.Start()
	for _, f := range frames {
		render.DrawFrame(f)
	}
	render.End()
}
