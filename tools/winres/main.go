package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
	"unicode/utf16"
)

const version = "1.1.0"

var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 256}

func main() {
	out := flag.String("o", "cmd/lokalka/rsrc_windows_amd64.syso", "куда записать ресурс")
	preview := flag.String("png", "", "сохранить ещё и PNG 256×256 для просмотра")
	flag.Parse()

	var frames [][]byte
	for _, size := range iconSizes {
		img := drawIcon(size)
		if size >= 256 {
			var buf bytes.Buffer
			if err := png.Encode(&buf, img); err != nil {
				log.Fatal(err)
			}
			frames = append(frames, buf.Bytes())
		} else {
			frames = append(frames, dib(img))
		}
		if size == 256 && *preview != "" {
			f, err := os.Create(*preview)
			if err != nil {
				log.Fatal(err)
			}
			if err := png.Encode(f, img); err != nil {
				log.Fatal(err)
			}
			f.Close()
		}
	}

	var res resources
	group := []byte{0, 0, 1, 0}
	group = binary.LittleEndian.AppendUint16(group, uint16(len(frames)))
	for i, frame := range frames {
		id := uint16(i + 1)
		res.add(rtIcon, id, frame)
		size := iconSizes[i]
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		group = append(group, dim, dim, 0, 0)
		group = binary.LittleEndian.AppendUint16(group, 1)
		group = binary.LittleEndian.AppendUint16(group, 32)
		group = binary.LittleEndian.AppendUint32(group, uint32(len(frame)))
		group = binary.LittleEndian.AppendUint16(group, id)
	}
	res.add(rtGroupIcon, 1, group)
	res.add(rtVersion, 1, versionInfo())

	if err := os.WriteFile(*out, res.coff(), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("записан %s", *out)
}

type pt struct{ x, y float64 }

var nodes = []pt{{128, 64}, {66, 166}, {190, 166}}

func drawIcon(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))

	nodeR, lineW := 27.0, 13.0
	if size <= 24 {
		nodeR, lineW = 36, 22
	} else if size <= 48 {
		nodeR, lineW = 31, 17
	}
	const ss = 5
	scale := 256 / float64(size)
	top := color.NRGBA{0x2a, 0x82, 0x5d, 0xff}
	bottom := color.NRGBA{0x18, 0x57, 0x3c, 0xff}
	glyph := [3]float64{0xf4, 0xf1, 0xea}
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var bgCov, glyphCov float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					u := (float64(px) + (float64(sx)+0.5)/ss) * scale
					v := (float64(py) + (float64(sy)+0.5)/ss) * scale
					if !inRoundRect(u, v, 6, 6, 250, 250, 58) {
						continue
					}
					bgCov++
					if inGlyph(u, v, nodeR, lineW) {
						glyphCov++
					}
				}
			}
			if bgCov == 0 {
				continue
			}
			t := (float64(py) + 0.5) / float64(size)
			base := [3]float64{
				lerp(float64(top.R), float64(bottom.R), t),
				lerp(float64(top.G), float64(bottom.G), t),
				lerp(float64(top.B), float64(bottom.B), t),
			}
			k := glyphCov / bgCov
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(math.Round(lerp(base[0], glyph[0], k))),
				G: uint8(math.Round(lerp(base[1], glyph[1], k))),
				B: uint8(math.Round(lerp(base[2], glyph[2], k))),
				A: uint8(math.Round(255 * bgCov / ss / ss)),
			})
		}
	}
	return img
}

func lerp(a, b, t float64) float64 { return a + (b-a)*t }

func inRoundRect(x, y, x0, y0, x1, y1, r float64) bool {
	if x < x0 || x > x1 || y < y0 || y > y1 {
		return false
	}
	cx := math.Max(x0+r, math.Min(x, x1-r))
	cy := math.Max(y0+r, math.Min(y, y1-r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r*r
}

func inGlyph(x, y, nodeR, lineW float64) bool {
	for _, n := range nodes {
		if (x-n.x)*(x-n.x)+(y-n.y)*(y-n.y) <= nodeR*nodeR {
			return true
		}
	}
	for i := range nodes {
		a, b := nodes[i], nodes[(i+1)%len(nodes)]
		if segDist(x, y, a, b) <= lineW/2 {
			return true
		}
	}
	return false
}

func segDist(x, y float64, a, b pt) float64 {
	dx, dy := b.x-a.x, b.y-a.y
	t := ((x-a.x)*dx + (y-a.y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	px, py := a.x+t*dx-x, a.y+t*dy-y
	return math.Sqrt(px*px + py*py)
}

func dib(img *image.NRGBA) []byte {
	w, h := img.Rect.Dx(), img.Rect.Dy()
	maskRow := (w + 31) / 32 * 4
	var b []byte
	b = binary.LittleEndian.AppendUint32(b, 40)
	b = binary.LittleEndian.AppendUint32(b, uint32(w))
	b = binary.LittleEndian.AppendUint32(b, uint32(h*2))
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 32)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(w*h*4+maskRow*h))
	b = append(b, make([]byte, 16)...)
	for y := h - 1; y >= 0; y-- {
		for x := 0; x < w; x++ {
			c := img.NRGBAAt(x, y)
			b = append(b, c.B, c.G, c.R, c.A)
		}
	}
	for y := h - 1; y >= 0; y-- {
		row := make([]byte, maskRow)
		for x := 0; x < w; x++ {
			if img.NRGBAAt(x, y).A == 0 {
				row[x/8] |= 0x80 >> (x % 8)
			}
		}
		b = append(b, row...)
	}
	return b
}

func versionInfo() []byte {
	fixed := binary.LittleEndian.AppendUint32(nil, 0xFEEF04BD)
	fixed = binary.LittleEndian.AppendUint32(fixed, 0x00010000)
	var v [3]uint32
	fmt.Sscanf(version, "%d.%d.%d", &v[0], &v[1], &v[2])
	for i := 0; i < 2; i++ {
		fixed = binary.LittleEndian.AppendUint32(fixed, v[0]<<16|v[1])
		fixed = binary.LittleEndian.AppendUint32(fixed, v[2]<<16)
	}
	fixed = binary.LittleEndian.AppendUint32(fixed, 0x3F)
	fixed = binary.LittleEndian.AppendUint32(fixed, 0)
	fixed = binary.LittleEndian.AppendUint32(fixed, 0x00040004)
	fixed = binary.LittleEndian.AppendUint32(fixed, 1)
	fixed = append(fixed, make([]byte, 12)...)

	strs := [][2]string{
		{"CompanyName", "Lokalka"},
		{"FileDescription", "Lokalka"},
		{"FileVersion", version},
		{"InternalName", "lokalka"},
		{"OriginalFilename", "Lokalka.exe"},
		{"ProductName", "Lokalka: локальная сеть для игр с друзьями"},
		{"ProductVersion", version},
	}
	var entries [][]byte
	for _, kv := range strs {
		val := utf16z(kv[1])
		entries = append(entries, verBlock(kv[0], 1, val, uint16(len(val)/2)))
	}
	table := verBlock("041904B0", 1, nil, 0, entries...)
	stringInfo := verBlock("StringFileInfo", 1, nil, 0, table)
	trans := verBlock("Translation", 0, []byte{0x19, 0x04, 0xB0, 0x04}, 4)
	varInfo := verBlock("VarFileInfo", 1, nil, 0, trans)
	return verBlock("VS_VERSION_INFO", 0, fixed, uint16(len(fixed)), stringInfo, varInfo)
}

func verBlock(key string, typ uint16, value []byte, valueLen uint16, children ...[]byte) []byte {
	b := make([]byte, 2)
	b = binary.LittleEndian.AppendUint16(b, valueLen)
	b = binary.LittleEndian.AppendUint16(b, typ)
	b = append(b, utf16z(key)...)
	b = pad(b, 4)
	b = append(b, value...)
	for _, c := range children {
		b = pad(b, 4)
		b = append(b, c...)
	}
	binary.LittleEndian.PutUint16(b, uint16(len(b)))
	return b
}

func utf16z(s string) []byte {
	var b []byte
	for _, u := range utf16.Encode([]rune(s)) {
		b = binary.LittleEndian.AppendUint16(b, u)
	}
	return append(b, 0, 0)
}

func pad(b []byte, n int) []byte {
	for len(b)%n != 0 {
		b = append(b, 0)
	}
	return b
}

const (
	rtIcon      = 3
	rtGroupIcon = 14
	rtVersion   = 16
	langRU      = 0x0419
)

type resource struct {
	typ, id uint16
	data    []byte
}

type resources struct{ list []resource }

func (r *resources) add(typ, id uint16, data []byte) {
	r.list = append(r.list, resource{typ, id, data})
}

func (r *resources) coff() []byte {
	var types []uint16
	byType := map[uint16][]resource{}
	for _, res := range r.list {
		if _, ok := byType[res.typ]; !ok {
			types = append(types, res.typ)
		}
		byType[res.typ] = append(byType[res.typ], res)
	}
	dirSize := func(n int) int { return 16 + 8*n }

	off := dirSize(len(types))
	typeDir := map[uint16]int{}
	for _, t := range types {
		typeDir[t] = off
		off += dirSize(len(byType[t]))
	}
	langDir := make([]int, len(r.list))
	order := []int{}
	for _, t := range types {
		for i, res := range r.list {
			if res.typ == t {
				langDir[i] = off
				off += dirSize(1)
				order = append(order, i)
			}
		}
	}
	entry := make([]int, len(r.list))
	for _, i := range order {
		entry[i] = off
		off += 16
	}
	dataAt := make([]int, len(r.list))
	for _, i := range order {
		off = (off + 7) &^ 7
		dataAt[i] = off
		off += len(r.list[i].data)
	}
	sec := make([]byte, (off+7)&^7)

	putDir := func(at, n int) {
		binary.LittleEndian.PutUint16(sec[at+14:], uint16(n))
	}
	putEntry := func(at int, id uint32, target int, sub bool) {
		binary.LittleEndian.PutUint32(sec[at:], id)
		v := uint32(target)
		if sub {
			v |= 0x80000000
		}
		binary.LittleEndian.PutUint32(sec[at+4:], v)
	}
	putDir(0, len(types))
	for k, t := range types {
		putEntry(16+8*k, uint32(t), typeDir[t], true)
		putDir(typeDir[t], len(byType[t]))
		n := 0
		for i, res := range r.list {
			if res.typ != t {
				continue
			}
			putEntry(typeDir[t]+16+8*n, uint32(res.id), langDir[i], true)
			n++
			putDir(langDir[i], 1)
			putEntry(langDir[i]+16, langRU, entry[i], false)
		}
	}
	var relocs []int
	for _, i := range order {
		binary.LittleEndian.PutUint32(sec[entry[i]:], uint32(dataAt[i]))
		binary.LittleEndian.PutUint32(sec[entry[i]+4:], uint32(len(r.list[i].data)))
		copy(sec[dataAt[i]:], r.list[i].data)
		relocs = append(relocs, entry[i])
	}

	const hdrSize = 20 + 40
	relocAt := hdrSize + len(sec)
	symAt := relocAt + 10*len(relocs)
	var b []byte
	b = binary.LittleEndian.AppendUint16(b, 0x8664)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(symAt))
	b = binary.LittleEndian.AppendUint32(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 0)

	b = append(b, ".rsrc\x00\x00\x00"...)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(sec)))
	b = binary.LittleEndian.AppendUint32(b, hdrSize)
	b = binary.LittleEndian.AppendUint32(b, uint32(relocAt))
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(relocs)))
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = binary.LittleEndian.AppendUint32(b, 0x40000040)

	b = append(b, sec...)
	for _, at := range relocs {
		b = binary.LittleEndian.AppendUint32(b, uint32(at))
		b = binary.LittleEndian.AppendUint32(b, 0)
		b = binary.LittleEndian.AppendUint16(b, 3)
	}
	b = append(b, ".rsrc\x00\x00\x00"...)
	b = binary.LittleEndian.AppendUint32(b, 0)
	b = binary.LittleEndian.AppendUint16(b, 1)
	b = binary.LittleEndian.AppendUint16(b, 0)
	b = append(b, 3, 0)
	return binary.LittleEndian.AppendUint32(b, 4)
}
