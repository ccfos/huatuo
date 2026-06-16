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

package flamegraph

import (
	"bytes"
	_ "embed"
	"fmt"
	"html"
	"io"
)

type frame struct {
	Children      []frame
	Name          string
	SampleCount   int64
	Depth         int     // 0 = outermost frame
	LeftPercent   float32 // absolute left-edge position as % of image width
	SamplePercent float32
}

// Style controls the visual appearance of the SVG flame graph.
type Style struct {
	ImageWidth    int
	FontFamily    string
	FontSize      int
	FontWidth     float32 // average glyph width relative to font size
	XMargin       float32
	FramesYOffset int
	FrameHeight   int
	FramePad      int
}

// Color returns a deterministic warm color for the given frame name.
func (s Style) Color(name string) string {
	v := randomNamehash(name)

	r := 205 + int(50*v)
	g := 0 + int(230*v)
	b := 0 + int(55*v)

	return fmt.Sprintf("rgb(%d,%d,%d)", r, g, b)
}

// DefaultStyle is the out-of-the-box SVG style.
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

// errWriter wraps an io.Writer and accumulates the first write error,
// allowing callers to chain multiple writes without per-call error checks.
type errWriter struct {
	w   io.Writer
	err error
}

func (ew *errWriter) printf(format string, args ...any) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintf(ew.w, format, args...)
}

func (ew *errWriter) println(s string) {
	if ew.err != nil {
		return
	}
	_, ew.err = fmt.Fprintln(ew.w, s)
}

type renderer struct {
	style         Style
	maxFrameDepth int
}

//go:embed svg.js
var javascript string

func newRenderer(style Style, maxFrameDepth int) renderer {
	return renderer{
		style:         style,
		maxFrameDepth: maxFrameDepth,
	}
}

func (r *renderer) Start(ew *errWriter) {
	topMargin := r.style.FontSize * 3
	bottomMargin := r.style.FontSize*2 + 10

	h := r.maxFrameDepth*r.style.FrameHeight + r.style.FramesYOffset + topMargin + bottomMargin
	w := r.style.ImageWidth

	ew.println(`<?xml version="1.0" standalone="no"?>`)
	ew.println(`<!DOCTYPE svg PUBLIC "-//W3C//DTD SVG 1.1//EN" "http://www.w3.org/Graphics/SVG/1.1/DTD/svg11.dtd">`)
	ew.printf(`<svg version="1.1" width="%d" height="%d" onload="init(evt)" viewBox="0 0 %d %d" xmlns="http://www.w3.org/2000/svg" xmlns:xlink="http://www.w3.org/1999/xlink">`+"\n", w, h, w, h)
	ew.println(`<defs >
	<linearGradient id="background" y1="0" y2="1" x1="0" x2="0" >
		<stop stop-color="#eeeeee" offset="5%" />
		<stop stop-color="#eeeeb0" offset="95%" />
	</linearGradient>
</defs>`)
	ew.println(`<style type="text/css">
	.func_g:hover { stroke:black; stroke-width:0.5; cursor:pointer; }
</style>`)
	ew.println(`<script type="text/ecmascript">
<![CDATA[`)
	ew.println(javascript)
	ew.println("]]>")
	ew.println("</script>")
	ew.printf(`<rect x="0.0" y="0" width="%d" height="%d" fill="url(#background)"/>`+"\n", w, h)
	ew.printf(`<text text-anchor="middle" x="%d" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)"  >Flame Graph</text>`+"\n", r.style.ImageWidth/2, r.style.FontSize*2, r.style.FontSize*3/2, r.style.FontFamily)
	ew.printf(`<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="details" > </text>`+"\n", r.style.XMargin, h-topMargin/2, r.style.FontSize, r.style.FontFamily)
	ew.printf(`<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="unzoom" onclick="unzoom()" style="opacity:0.0;cursor:pointer" >Reset Zoom</text>`+"\n", r.style.XMargin, r.style.FontSize*2, r.style.FontSize, r.style.FontFamily)
	ew.printf(`<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="search" onmouseover="searchover()" onmouseout="searchout()" onclick="search_prompt()" style="opacity:0.1;cursor:pointer" >Search</text>`+"\n", float32(r.style.ImageWidth)-r.style.XMargin-100, r.style.FontSize*2, r.style.FontSize, r.style.FontFamily)
	ew.printf(`<text text-anchor="" x="%.1f" y="%d" font-size="%d" font-family="%s" fill="rgb(0,0,0)" id="matched" > </text>`+"\n", float32(r.style.ImageWidth)-r.style.XMargin-100, h-bottomMargin/2, r.style.FontSize, r.style.FontFamily)
}

func (r *renderer) End(ew *errWriter) {
	ew.println(`</svg>`)
}

func (r *renderer) DrawFrame(f frame, ew *errWriter) {
	queue := make([]frame, 0, 1000)
	queue = append(queue, f)

	for len(queue) > 0 {
		r.drawFrame(queue[0], ew)
		queue = append(queue, queue[0].Children...)
		queue = queue[1:]
	}
}

func (r *renderer) drawFrame(f frame, ew *errWriter) {
	title := fmt.Sprintf("%s (%d samples, %.2f%%)", f.Name, f.SampleCount, f.SamplePercent)
	color := r.style.Color(f.Name)

	usableWidth := float32(r.style.ImageWidth) - r.style.XMargin*2

	w := f.SamplePercent / 100 * usableWidth
	h := r.style.FrameHeight
	x := r.style.XMargin + (f.LeftPercent / 100 * usableWidth)
	y := r.style.FramesYOffset + (r.maxFrameDepth-f.Depth-1)*r.style.FrameHeight

	ew.printf(`<g class="func_g" onmouseover="s('%s')" onmouseout="c()" onclick="zoom(this)">`+"\n", title)
	ew.printf("  <title>%s</title>\n", title)
	ew.printf(`  <rect x="%.1f" y="%d" width="%.1f" height="%d" fill="%s" rx="2" ry="2"/>`+"\n",
		x, y, w, h, color)

	text := r.fitText(f.Name, w)

	ew.printf(`  <text text-anchor="" x="%.1f" y="%.1f" font-size="%d" font-family="%s" fill="rgb(0,0,0)">%s</text>`+"\n",
		x+3, 3+float32(y)+float32(h)/2, r.style.FontSize, r.style.FontFamily, text)

	ew.printf("</g>\n")
}

// randomNamehash maps a frame name to a deterministic float32 in [0, 1).
// Uses a simple multiplicative hash to avoid per-call allocations.
func randomNamehash(n string) float32 {
	var h uint32 = 2166136261 // FNV-1a offset basis
	for i := 0; i < len(n); i++ {
		h ^= uint32(n[i])
		h *= 16777619
	}
	return float32(h&0xFFFF) / 0x10000
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

	// Truncate to available width; assumes ASCII.
	_, _ = buf.WriteString(text[:avail-2])
	_, _ = buf.WriteString("..")

	return html.EscapeString(buf.String())
}

// Render writes a flame graph SVG for stacks using the default style.
func Render(stacks []Stack, out io.Writer) error {
	return RenderStyle(stacks, out, DefaultStyle)
}

// RenderStyle writes a flame graph SVG for stacks using the given style.
func RenderStyle(stacks []Stack, out io.Writer, style Style) error {
	var proc processor

	for i := range stacks {
		proc.Process(stacks[i])
	}

	proc.Finalize()

	frames, maxDepth := proc.Result()
	render := newRenderer(style, maxDepth)
	ew := &errWriter{w: out}

	render.Start(ew)

	for i := range frames {
		render.DrawFrame(frames[i], ew)
	}

	render.End(ew)

	return ew.err
}
