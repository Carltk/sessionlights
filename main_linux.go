package main

// Linux: the bar lives in the top panel as StatusNotifierItems, because
// Wayland lets no client keep a floating window on top. Stats tick every two
// seconds; session names redraw on a light-file change.
//
//	sessionlights            run the bar
//	sessionlights hook       write a light file from a Claude Code hook event
//	sessionlights -png f     render the bar once to a PNG and exit

import (
	"bytes"
	"crypto/sha256"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/godbus/dbus/v5"
)

const (
	busName  = "io.github.carltk.SessionLights"
	statTick = 2 * time.Second
	segGapPx = 0.5 // gap between segments in "one" layout, as a share of height
)

// widthTemplates hold each segment's widest likely text, so the item keeps
// its width while the numbers change.
var widthTemplates = map[string]segment{
	segCPU: {Runs: []run{{Text: "C-99%"}}},
	segGPU: {Runs: []run{{Text: "G-99%"}}},
	segMem: {Runs: []run{{Text: "M-99.9/16"}}},
	segNet: {Runs: []run{{Arrow: -1}, {Text: "999K "}, {Arrow: 1}, {Text: "999K"}}},
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		runHookMain(newProbe())
		return
	}
	dir := flag.String("dir", lightsDir(), "light files directory")
	layout := flag.String("layout", "split", `"split": one panel item per segment; "one": a single item`)
	pngOut := flag.String("png", "", "render the bar once to this PNG (at 32 px) and exit")
	logFile := flag.String("log", "", "write a debug log to this file")
	once := flag.Duration("once", 0, "print one sample taken over this interval and exit")
	height := flag.Int("height", 22, "segment height in pixels (the panel is usually 32)")
	flag.Parse()

	log.SetOutput(os.Stderr)
	if *logFile != "" {
		if f, err := os.Create(*logFile); err == nil {
			log.SetOutput(f)
		}
	}

	b := &bar{dir: *dir, layout: *layout, smp: newSampler(), proc: procInfo(),
		height: *height, files: map[string]string{}, prevFiles: map[string]string{}, drawn: map[string]string{}}
	if *once > 0 {
		b.smp.Sample(time.Now())
		time.Sleep(*once)
		st := b.smp.Sample(time.Now())
		fmt.Printf("cpu=%.1f gpu=%.1f mem_used=%d mem_total=%d rx_bps=%.0f tx_bps=%.0f\n",
			st.CPU, st.GPU, st.MemUsed, st.MemTotal, st.RxBps, st.TxBps)
		return
	}
	if *pngOut != "" {
		b.smp.Sample(time.Now())
		time.Sleep(time.Second)
		b.stats = b.smp.Sample(time.Now())
		b.loadLights()
		if err := b.writePNG(*pngOut, *height); err != nil {
			log.Fatal(err)
		}
		return
	}
	if err := b.run(); err != nil {
		log.Fatal(err)
	}
}

type bar struct {
	dir, layout string
	smp         *sampler
	proc        Proc

	stats  Stats
	gpuOK  bool
	sess   segment // session names, coloured by state
	names  []string
	items  map[string]*trayItem
	itemsL []*trayItem

	height    int    // icon height in px
	iconDir   string // where segment PNGs are written
	files     map[string]string
	prevFiles map[string]string
	drawn     map[string]string // segment key last rendered, per item
}

func (b *bar) loadLights() {
	b.sess, b.names = sessionView(LoadLights(b.dir), b.proc, time.Now())
}

// segments is what to draw, per item key, in bar order.
func (b *bar) segments() ([]string, map[string]segment) {
	segs := statSegments(b.stats, b.gpuOK || b.stats.GPU >= 0 || len(b.smp.drmFds) > 0)
	if len(b.sess.Runs) > 0 {
		segs[segSessions] = b.sess
	}
	var keys []string
	for _, k := range segOrder {
		if _, ok := segs[k]; ok {
			keys = append(keys, k)
		}
	}
	return keys, segs
}

// composite draws every segment side by side into one bitmap.
func (b *bar) composite(h int) *image.NRGBA {
	keys, segs := b.segments()
	gap := int(float64(h) * segGapPx)
	var imgs []*image.NRGBA
	w := 0
	for i, k := range keys {
		img := renderSegment(segs[k], h, measure(widthTemplates[k], h))
		imgs = append(imgs, img)
		if i > 0 {
			w += gap
		}
		w += img.Bounds().Dx()
	}
	out := image.NewNRGBA(image.Rect(0, 0, max(w, 1), h))
	x := 0
	for i, img := range imgs {
		if i > 0 {
			x += gap
		}
		draw.Draw(out, img.Bounds().Add(image.Pt(x, 0)), img, image.Point{}, draw.Src)
		x += img.Bounds().Dx()
	}
	return out
}

func (b *bar) writePNG(path string, h int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, b.composite(h))
}

func (b *bar) run() error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	reply, err := conn.RequestName(busName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		log.Print("a bar is already running; exiting")
		return nil
	}

	// Icons live in the runtime dir: tmpfs, private to this user, and inside
	// the area GNOME's host treats as user-writable (so it skips its cache).
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = os.TempDir()
	}
	b.iconDir = filepath.Join(rt, "sessionlights")
	if err := os.MkdirAll(b.iconDir, 0o700); err != nil {
		return err
	}
	if old, _ := filepath.Glob(filepath.Join(b.iconDir, "*.png")); len(old) > 0 {
		for _, f := range old {
			os.Remove(f)
		}
	}

	b.items = map[string]*trayItem{}
	keys := []string{"bar"}
	if b.layout == "split" {
		keys = segOrder
	}
	for i, k := range keys {
		it, err := newTrayItem(conn, itemPath(k), "sessionlights-"+k, uint32(i))
		if err != nil {
			return err
		}
		b.items[k] = it
		b.itemsL = append(b.itemsL, it)
	}
	if err := watchWatcher(conn, b.registerOrder); err != nil {
		log.Printf("watcher restarts will not be followed: %v", err)
	}

	b.stats = b.smp.Sample(time.Now())
	b.loadLights()
	b.draw()
	for _, it := range b.registerOrder() {
		if err := it.register(); err != nil {
			log.Printf("register %s: %v (no tray host yet; will register when one appears)", it.path, err)
		}
	}

	_ = os.MkdirAll(b.dir, 0o755)
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Add(b.dir); err != nil {
		log.Printf("watch %s: %v", b.dir, err)
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	stats := time.NewTicker(statTick)
	defer stats.Stop()
	// The slow tick catches what no file change announces: a session dying.
	slow := time.NewTicker(slowLightsTick)
	defer slow.Stop()
	for {
		select {
		case <-stop:
			return nil
		case <-stats.C:
			b.stats = b.smp.Sample(time.Now())
			if b.stats.GPU >= 0 {
				b.gpuOK = true
			}
			b.draw()
		case <-slow.C:
			b.loadLights()
			b.draw()
		case ev := <-w.Events:
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) != 0 {
				b.loadLights()
				b.draw()
			}
		case err := <-w.Errors:
			log.Printf("watch: %v", err)
		}
	}
}

const slowLightsTick = 5 * time.Second

// registerOrder is the bar order reversed: GNOME's host inserts each new item
// at the left end of the status area, so the last registered shows first.
func (b *bar) registerOrder() []*trayItem {
	out := make([]*trayItem, len(b.itemsL))
	for i, it := range b.itemsL {
		out[len(out)-1-i] = it
	}
	return out
}

// draw renders each segment to a PNG and points its item at it. A file is
// written only when the pixels changed; its name carries a hash of them,
// because the host caches icons by name. The previous file is kept one round
// so the host never loads a file that was just deleted.
func (b *bar) draw() {
	// The hover text names the sessions only. Putting the live numbers in it
	// made every item send a title change each tick, and the host fetches
	// three properties back for each: most of the bar's idle cost.
	title := sessionsTitle(b.names)
	if b.layout != "split" {
		b.publish("bar", b.items["bar"], b.composite(b.height), title)
		return
	}
	_, segs := b.segments()
	for _, k := range segOrder {
		it := b.items[k]
		s, ok := segs[k]
		if !ok {
			it.SetActive(false)
			continue
		}
		it.SetActive(true) // first: a hidden item's host ignores its icon changes
		if sk := s.key(); sk != b.drawn[k] {
			minW := hostSafeWidth(max(measure(s, b.height), measure(widthTemplates[k], b.height)), b.height)
			b.publish(k, it, renderSegment(s, b.height, minW), title)
			b.drawn[k] = sk
		}
		it.SetTitle(title)
	}
}

// pngEnc trades a few hundred bytes of tmpfs for most of the encode cost,
// and reuses its compressor instead of allocating one per icon.
var pngEnc = png.Encoder{CompressionLevel: png.BestSpeed, BufferPool: &pngPool{}}

type pngPool struct{ b *png.EncoderBuffer }

func (p *pngPool) Get() *png.EncoderBuffer  { return p.b }
func (p *pngPool) Put(b *png.EncoderBuffer) { p.b = b }

func (b *bar) publish(key string, it *trayItem, img *image.NRGBA, title string) {
	var buf bytes.Buffer
	if err := pngEnc.Encode(&buf, img); err != nil {
		return
	}
	sum := sha256.Sum256(buf.Bytes())
	path := filepath.Join(b.iconDir, fmt.Sprintf("%s-%x.png", key, sum[:6]))
	if path != b.files[key] {
		if err := os.WriteFile(path+".tmp", buf.Bytes(), 0o644); err == nil && os.Rename(path+".tmp", path) == nil {
			if old := b.prevFiles[key]; old != "" && old != path {
				os.Remove(old)
			}
			b.prevFiles[key] = b.files[key]
			b.files[key] = path
			it.SetIconFile(path)
		}
	}
	it.SetTitle(title)
}
