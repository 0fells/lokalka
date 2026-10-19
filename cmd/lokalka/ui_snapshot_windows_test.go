//go:build windows

package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"lokalka/internal/proto"
	"lokalka/internal/vpn"
)

var (
	procPrintWindow      = user32.NewProc("PrintWindow")
	procCreateCompatDC   = gdi32.NewProc("CreateCompatibleDC")
	procCreateDIBSection = gdi32.NewProc("CreateDIBSection")
	procGdiFlush         = gdi32.NewProc("GdiFlush")
	procDeleteDC         = gdi32.NewProc("DeleteDC")
	procIsWindow         = user32.NewProc("IsWindow")
)

type bitmapInfoHeader struct {
	size         uint32
	width        int32
	height       int32
	planes       uint16
	bitCount     uint16
	compression  uint32
	sizeImage    uint32
	xPels, yPels int32
	clrUsed      uint32
	clrImportant uint32
	colors       [4]byte
}

func snapshotDir(t *testing.T) string {
	dir := os.Getenv("LOKALKA_UI_SNAPSHOT")
	if dir == "" {
		t.Skip("LOKALKA_UI_SNAPSHOT не задан")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func capture(t *testing.T, hwnd uintptr, path string) {
	t.Helper()
	var wr winRect
	procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
	w, h := wr.right-wr.left, wr.bottom-wr.top
	if w <= 0 || h <= 0 {
		t.Fatalf("пустое окно %v", wr)
	}
	screen, _, _ := procGetDC.Call(0)
	defer procReleaseDC.Call(0, screen)
	mem, _, _ := procCreateCompatDC.Call(screen)
	defer procDeleteDC.Call(mem)
	hdr := bitmapInfoHeader{size: 40, width: w, height: -h, planes: 1, bitCount: 32}
	var bits uintptr
	bmp, _, _ := procCreateDIBSection.Call(mem, uintptr(unsafe.Pointer(&hdr)), 0, uintptr(unsafe.Pointer(&bits)), 0, 0)
	if bmp == 0 || bits == 0 {
		t.Fatal("CreateDIBSection не сработал")
	}
	defer procDeleteObject.Call(bmp)
	buf := unsafe.Slice((*byte)(unsafe.Pointer(bits)), int(w)*int(h)*4)
	old, _, _ := procSelectObject.Call(mem, bmp)
	for _, flag := range []uintptr{2, 0} {
		ok, _, _ := procPrintWindow.Call(hwnd, mem, flag)
		procGdiFlush.Call()
		if ok != 0 && !allBlack(buf) {
			break
		}
		t.Logf("PrintWindow flag=%d ok=%d: пусто", flag, ok)
	}
	procSelectObject.Call(mem, old)
	img := image.NewRGBA(image.Rect(0, 0, int(w), int(h)))
	for i := 0; i+3 < len(buf); i += 4 {
		img.Pix[i+0] = buf[i+2]
		img.Pix[i+1] = buf[i+1]
		img.Pix[i+2] = buf[i+0]
		img.Pix[i+3] = 0xff
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
}

func allBlack(buf []byte) bool {
	for i := 0; i+3 < len(buf); i += 4 {
		if buf[i]|buf[i+1]|buf[i+2] != 0 {
			return false
		}
	}
	return true
}

func fakeAdminNets() []proto.AdminNet {
	now := time.Now().Unix()
	return []proto.AdminNet{
		{
			Name:   "friends",
			Online: 2,
			Peers: []proto.AdminPeer{
				{IP: "10.66.2.2", Hostname: "DESKTOP-ALEX", Endpoint: "198.51.100.24:61234", Joined: now - 3700, Path: "udp", Config: "friends"},
				{IP: "10.66.2.3", Hostname: "NOTEBOOK-KATYA", Endpoint: "198.51.100.77:50111", Joined: now - 120, Path: "relay", Config: "пк-2"},
			},
			Blocked: []proto.AdminPeer{{IP: "10.66.2.9", Hostname: "OLD-PC"}},
			Configs: []proto.AdminCfg{
				{ID: "a1", Label: "friends", Created: now - 86400*3, LastSeen: now - 3700, LastHost: "DESKTOP-ALEX"},
				{ID: "a2", Label: "пк-2", Created: now - 86400, LastSeen: now - 120, LastHost: "NOTEBOOK-KATYA"},
				{ID: "a3", Label: "пк-3", Created: now - 600},
			},
			Panels: []proto.AdminPanel{
				{ID: "p1", Label: "Вася", Created: now - 86400, LastSeen: now - 300, LastHost: "198.51.100.24", Online: true},
				{ID: "p2", Label: "Петя", Created: now - 600},
				{ID: "p3", Label: "Маша", Created: now - 86400*2, LastSeen: now - 7200, LastHost: "198.51.100.77", Disabled: true},
			},
		},
		{Name: "coop", Configs: []proto.AdminCfg{{ID: "b1", Label: "coop", Created: now - 86400*20}}},
	}
}

func waitWindow(get func() uintptr) uintptr {
	for i := 0; i < 100; i++ {
		if h := get(); h != 0 {
			return h
		}
		time.Sleep(50 * time.Millisecond)
	}
	return 0
}

func TestAdminSnapshot(t *testing.T) {
	dir := snapshotDir(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	admSess = nil
	admNets = fakeAdminNets()
	admErr = nil
	done := make(chan struct{})
	go func() {
		defer close(done)
		hwnd := waitWindow(func() uintptr { return admHwnd })
		if hwnd == 0 {
			t.Error("окно не появилось")
			return
		}
		pokeApply(hwnd)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "admin-1-normal.png"))
		text, _ := syscallUTF16("123123123")
		procSendMessage.Call(admFilter, 0x000C, 0, uintptr(unsafe.Pointer(text)))
		time.Sleep(400 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "admin-2-filter.png"))
		empty, _ := syscallUTF16("")
		procSendMessage.Call(admFilter, 0x000C, 0, uintptr(unsafe.Pointer(empty)))
		procShowWindow.Call(hwnd, 3)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "admin-3-max.png"))
		procShowWindow.Call(hwnd, 9)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "admin-4-restored.png"))
		procMoveWindow.Call(hwnd, 40, 40, 980, 720, 1)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "admin-5-min.png"))
		procPostMessage.Call(hwnd, wmCommand, idAdmTabPanels, 0)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "admin-6-panels.png"))
		if !admTabPanels {
			t.Error("вкладка \"Панели\" не открылась")
		}
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}()
	if err := adminWindow(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestClientSnapshot(t *testing.T) {
	dir := snapshotDir(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	statusSource = func() vpn.ClientStatus {
		return vpn.ClientStatus{
			State:     "Подключено",
			Network:   "friends",
			Hostname:  "DESKTOP-ALEX",
			VirtualIP: "10.66.2.2",
			Peers: []vpn.PeerStatus{
				{IP: "10.66.2.3", Hostname: "NOTEBOOK-KATYA", Direct: true},
				{IP: "10.66.2.4", Hostname: "GAMER-PC", Direct: false},
			},
		}
	}
	defer func() { statusSource = nil }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hwnd := waitWindow(func() uintptr { return mainHwnd })
		if hwnd == 0 {
			t.Error("окно не появилось")
			return
		}
		time.Sleep(1500 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "client-1-normal.png"))
		procShowWindow.Call(hwnd, 3)
		time.Sleep(1200 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "client-2-max.png"))
		procShowWindow.Call(hwnd, 9)
		time.Sleep(1200 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "client-3-restored.png"))
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}()
	if err := statusWindow(); err != nil {
		t.Fatal(err)
	}
	<-done
}

func TestLoginSnapshot(t *testing.T) {
	dir := snapshotDir(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	done := make(chan struct{})
	go func() {
		defer close(done)
		hwnd := waitWindow(func() uintptr { return dlg.hwnd })
		if hwnd == 0 {
			t.Error("окно не появилось")
			return
		}
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "login-1.png"))
		if hint := windowText(dlg.hint); hint != "" {
			t.Errorf("на свежей форме входа подсказка %q", hint)
		}
		procPostMessage.Call(dlg.editB, wmKeyDown, vkEscape, 0)
	}()
	if _, err := loginAdmin(); err == nil {
		t.Error("Esc должен закрывать окно входа без сессии")
	}
	<-done
}

func TestFormSnapshotAndEnter(t *testing.T) {
	dir := snapshotDir(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	go func() {
		hwnd := waitWindow(func() uintptr { return dlg.hwnd })
		if hwnd == 0 {
			t.Error("диалог не появился")
			return
		}
		time.Sleep(600 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "form-1.png"))
		procPostMessage.Call(dlg.editA, wmKeyDown, vkReturn, 0)
	}()
	a, b, ok := askForm("Новая сеть", "Имя сети", "Пароль", "friends", true)
	if !ok || a != "friends" || b != "" {
		t.Fatalf("Enter не отправил форму: %q %q %v", a, b, ok)
	}
}

func syscallUTF16(s string) (*uint16, error) {
	return syscall.UTF16PtrFromString(s)
}

func TestPanelWindowSnapshot(t *testing.T) {
	dir := snapshotDir(t)
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	admLimited, admOwner = true, "Вася"
	defer func() { admLimited, admOwner = false, "" }()
	admSess = nil
	nets := fakeAdminNets()[:1]
	nets[0].Peers = nets[0].Peers[1:]
	nets[0].Panels = nil
	admNets = nets
	admErr = nil
	done := make(chan struct{})
	go func() {
		defer close(done)
		hwnd := waitWindow(func() uintptr { return admHwnd })
		if hwnd == 0 {
			t.Error("окно не появилось")
			return
		}
		pokeApply(hwnd)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "panel-1-normal.png"))
		procMoveWindow.Call(hwnd, 40, 40, 760, 560, 1)
		time.Sleep(700 * time.Millisecond)
		capture(t, hwnd, filepath.Join(dir, "panel-2-min.png"))
		procPostMessage.Call(hwnd, wmClose, 0, 0)
	}()
	if err := adminWindow(); err != nil {
		t.Fatal(err)
	}
	<-done
}
