//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"lokalka/internal/vpn"
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsFixedWindow      = 0x00C00000 | 0x00080000 | 0x00020000
	wsVisible          = 0x10000000
	wsChild            = 0x40000000
	wsVScroll          = 0x00200000
	wsClipChildren     = 0x02000000
	csRedraw           = 0x0001 | 0x0002
	prettyList         = wsChild | wsVisible | wsVScroll | 0x0010 | 0x0040 | 0x0100 | 1
	esPassword         = 0x0020
	wmChar             = 0x0102
	wmKeyDown          = 0x0100
	wmPaint            = 0x000F
	wmNcDestroy        = 0x0082
	wmLButtonDown      = 0x0201
	gwlpWndProc        = ^uintptr(3)
	wmDestroy          = 0x0002
	wmClose            = 0x0010
	wmCommand          = 0x0111
	wmSetFont          = 0x0030
	wmTimer            = 0x0113
	lbResetContent     = 0x0184
	lbAddString        = 0x0180
	enSetFocus         = 0x0100
	enKillFocus        = 0x0200
	idOK               = 1
	idExit             = 2
	idCopyMyIP         = 3
	vkReturn           = 13
	vkEscape           = 27
)

var (
	user32 = syscall.NewLazyDLL("user32.dll")
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	k32    = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClass    = user32.NewProc("RegisterClassExW")
	procCreateWindow     = user32.NewProc("CreateWindowExW")
	procDefWindow        = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procShowWindow       = user32.NewProc("ShowWindow")
	procUpdateWindow     = user32.NewProc("UpdateWindow")
	procGetMessage       = user32.NewProc("GetMessageW")
	procTranslate        = user32.NewProc("TranslateMessage")
	procDispatch         = user32.NewProc("DispatchMessageW")
	procPostQuit         = user32.NewProc("PostQuitMessage")
	procPostMessage      = user32.NewProc("PostMessageW")
	procSetText          = user32.NewProc("SetWindowTextW")
	procGetText          = user32.NewProc("GetWindowTextW")
	procSendMessage      = user32.NewProc("SendMessageW")
	procSetTimer         = user32.NewProc("SetTimer")
	procLoadCursor       = user32.NewProc("LoadCursorW")
	procGetModule        = k32.NewProc("GetModuleHandleW")
	procGetConsole       = k32.NewProc("GetConsoleWindow")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procGlobalAlloc      = k32.NewProc("GlobalAlloc")
	procGlobalLock       = k32.NewProc("GlobalLock")
	procGlobalUnlock     = k32.NewProc("GlobalUnlock")
	procStockObject      = gdi32.NewProc("GetStockObject")
	procMessageBox       = user32.NewProc("MessageBoxW")
	procFillRect         = user32.NewProc("FillRect")
	procDrawText         = user32.NewProc("DrawTextW")
	procGetClientRect    = user32.NewProc("GetClientRect")
	procMoveWindow       = user32.NewProc("MoveWindow")
	procCreateFont       = gdi32.NewProc("CreateFontW")
	procCreateBrush      = gdi32.NewProc("CreateSolidBrush")
	procSetTextColor     = gdi32.NewProc("SetTextColor")
	procSetBkColor       = gdi32.NewProc("SetBkColor")
	procSetBkMode        = gdi32.NewProc("SetBkMode")
	procSelectObject     = gdi32.NewProc("SelectObject")
	procDeleteObject     = gdi32.NewProc("DeleteObject")
	procRoundRect        = gdi32.NewProc("RoundRect")
	procRectangle        = gdi32.NewProc("Rectangle")
	procCreatePen        = gdi32.NewProc("CreatePen")
	procInvalidateRect   = user32.NewProc("InvalidateRect")
	procRedrawWindow     = user32.NewProc("RedrawWindow")
	procGetWindowRect    = user32.NewProc("GetWindowRect")
	procMapPoints        = user32.NewProc("MapWindowPoints")
	procGetParent        = user32.NewProc("GetParent")
	procSetWindowPos     = user32.NewProc("SetWindowPos")
	procEnableWindow     = user32.NewProc("EnableWindow")
	procSetFocus         = user32.NewProc("SetFocus")
	procGetFocus         = user32.NewProc("GetFocus")
	procSetForeground    = user32.NewProc("SetForegroundWindow")
	procSystemMetrics    = user32.NewProc("GetSystemMetrics")
	procTextLength       = user32.NewProc("GetWindowTextLengthW")
	procGetDC            = user32.NewProc("GetDC")
	procReleaseDC        = user32.NewProc("ReleaseDC")
	procSetLongPtr       = user32.NewProc("SetWindowLongPtrW")
	procCallWndProc      = user32.NewProc("CallWindowProcW")
	procTextExtent       = gdi32.NewProc("GetTextExtentPoint32W")
	procLoadImage        = user32.NewProc("LoadImageW")
	procDrawIconEx       = user32.NewProc("DrawIconEx")
	procIsWindow         = user32.NewProc("IsWindow")
	procWindowThread     = user32.NewProc("GetWindowThreadProcessId")
	procCurrentThread    = k32.NewProc("GetCurrentThreadId")

	uiSource   = map[uintptr]string{}
	uiLangFlip uint32

	appIcons   = map[int]uintptr{}
	brandMarks = map[uintptr]bool{}
	ipCard     winRect
	stateDot   winRect

	editOrig    = map[uintptr]uintptr{}
	fields      = map[uintptr]*field{}
	fieldLine   int32
	uiFields    uint32
	focusWant   uintptr
	clearFilter uint32

	guiClient    *vpn.Client
	guiCancel    context.CancelFunc
	hwndMark     uintptr
	hwndState    uintptr
	hwndIP       uintptr
	hwndNet      uintptr
	hwndPeers    uintptr
	hwndList     uintptr
	hwndExit     uintptr
	pwdEdit      uintptr
	pwdHint      uintptr
	pwdHwnd      uintptr
	pwdValue     string
	pwdOK        bool
	pwdClick     uint32
	pwdClose     uint32
	mainHwnd     uintptr
	mainClose    uint32
	uiStatus     uint32
	uiMainLayout uint32
	guiSig       string
	keepProc     []uintptr
	statusSource func() vpn.ClientStatus
	hwndCopyIP   uintptr
	hwndLang     uintptr
	clientIPs    []string
	myIP         string
	clientNote   string
	clientNoteAt time.Time
	uiClientCmd  uintptr
)

func guiSupported() bool { return true }

type wndClass struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menu       *uint16
	className  *uint16
	iconSm     uintptr
}

type winMsg struct {
	hwnd    uintptr
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	x       int32
	y       int32
	pad     uint32
}

func hideConsoleWindow() {
	hwnd, _, _ := procGetConsole.Call()
	if hwnd != 0 {
		procShowWindow.Call(hwnd, 0)
	}
}

func runGUI(cfg vpn.Config) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hideConsoleWindow()
	if cfg.Password == "" {
		password, ok := askPassword()
		if !ok {
			return nil
		}
		cfg.Password = password
	}
	client := vpn.New(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	guiCancel = cancel
	guiClient = client
	errCh := make(chan error, 1)
	go func() { errCh <- client.Run(ctx) }()
	if err := statusWindow(); err != nil {
		cancel()
		return err
	}
	cancel()
	err := <-errCh
	if err == context.Canceled {
		return nil
	}
	return err
}

func askPassword() (string, bool) {
	pwdOK = false
	pwdValue = ""
	atomic.StoreUint32(&pwdClick, 0)
	atomic.StoreUint32(&pwdClose, 0)
	ensureUI()
	hwnd := newWindow("LokalkaPwd", pwdProc, "Lokalka", 440, 300, false, 0)
	if hwnd == 0 {
		return "", false
	}
	pwdHwnd = hwnd
	brandMark(hwnd, 28, 22, 368-langW-12, 34)
	langButton(hwnd, 28, 24, 30)
	pwdHint = control(hwnd, "STATIC", "Пароль сети", 28, 68, 368, 22, wsChild|wsVisible, 0, admFont)
	pwdEdit = makeField(hwnd, "", 28, 94, 368, 40, true, 0, "")
	admButton(hwnd, "Войти", 28, 154, 160, 42, idOK)
	show(hwnd)
	procSetFocus.Call(pwdEdit)
	messageLoop()
	return pwdValue, pwdOK
}

func statusWindow() error {
	ensureUI()
	guiSig = ""
	clientNote = ""
	atomic.StoreUint32(&mainClose, 0)
	hwnd := newWindow(clientWindowClass, mainProc, "Lokalka", 640, 600, true, 0)
	if hwnd == 0 {
		return fmt.Errorf("не удалось открыть окно")
	}
	mainHwnd = hwnd
	hwndMark = brandMark(hwnd, 28, 18, 560, 34)
	hwndState = control(hwnd, "STATIC", "Подключение...", 46, 60, 540, 24, wsChild|wsVisible, 0, admFontSemi)
	setCtlFont(hwndState, admFontSemi)
	hwndNet = control(hwnd, "STATIC", "Ваш адрес в сети", 48, 114, 340, 22, wsChild|wsVisible, 0, admFont)
	hwndIP = control(hwnd, "STATIC", "ещё не выдан", 48, 138, 340, 36, wsChild|wsVisible, 0, admFontTitle)
	setCtlFont(hwndIP, admFontTitle)
	hwndPeers = control(hwnd, "STATIC", "Участники", 28, 206, 560, 22, wsChild|wsVisible, 0, admFontSemi)
	setCtlFont(hwndPeers, admFontSemi)
	hwndList = control(hwnd, "LISTBOX", "", 28, 232, 560, 220, prettyList, idClientList, admFont)
	hwndCopyIP = admButton(hwnd, "Скопировать IP", 398, 124, 170, 40, idCopyMyIP)
	hwndExit = admButton(hwnd, "Выход", 240, 480, 150, 40, idExit)
	hwndLang = langButton(hwnd, 28, 20, 30)
	layoutMain(hwnd)
	procSetTimer.Call(hwnd, 1, 1000, 0)
	refreshStatus()
	show(hwnd)
	messageLoop()
	mainHwnd = 0
	return nil
}

func newWindow(class string, proc func(uintptr, uintptr, uintptr, uintptr) uintptr, title string, width, height uintptr, resizable bool, owner uintptr) uintptr {
	ensureUI()
	instance, _, _ := procGetModule.Call(0)
	cursor, _, _ := procLoadCursor.Call(0, 32512)
	className, _ := syscall.UTF16PtrFromString(class)
	cb := syscall.NewCallback(proc)
	keepProc = append(keepProc, cb)
	background := bgBrush
	if background == 0 {
		background = 6
	}
	bigIcon, _, _ := procSystemMetrics.Call(11)
	smallIcon, _, _ := procSystemMetrics.Call(49)
	wc := wndClass{
		size:       uint32(unsafe.Sizeof(wndClass{})),
		style:      csRedraw,
		wndProc:    cb,
		instance:   instance,
		icon:       appIcon(int(bigIcon)),
		cursor:     cursor,
		background: background,
		className:  className,
		iconSm:     appIcon(int(smallIcon)),
	}
	procRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
	titlePtr, _ := syscall.UTF16PtrFromString(tr(title))
	style := uintptr(wsOverlappedWindow | wsClipChildren)
	if !resizable {
		style = wsFixedWindow | wsClipChildren
	}
	x, y := uintptr(0x80000000), uintptr(0x80000000)
	if cx, cy, ok := centerOver(owner, int32(width), int32(height)); ok {
		x, y = uintptr(cx), uintptr(cy)
	}
	hwnd, _, _ := procCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(titlePtr)),
		style,
		x, y, width, height,
		owner, 0, instance, 0,
	)
	if hwnd != 0 {
		uiSource[hwnd] = title
	}
	return hwnd
}

const brandGap = 42

func appIcon(size int) uintptr {
	if size <= 0 {
		size = 32
	}
	if h, ok := appIcons[size]; ok {
		return h
	}
	instance, _, _ := procGetModule.Call(0)
	h, _, _ := procLoadImage.Call(instance, 1, 1, uintptr(size), uintptr(size), 0)
	appIcons[size] = h
	return h
}

func brandMark(parent uintptr, x, y, w, h int) uintptr {
	mark := control(parent, "STATIC", "Lokalka", x+brandGap, y, w-brandGap, h, wsChild|wsVisible, 0, admFontTitle)
	setCtlFont(mark, admFontTitle)
	brandMarks[mark] = true
	return mark
}

func paintBrand(hwnd, hdc uintptr) {
	icon := appIcon(32)
	for mark := range brandMarks {
		parent, _, _ := procGetParent.Call(mark)
		if parent == 0 {
			delete(brandMarks, mark)
			continue
		}
		if parent != hwnd || icon == 0 {
			continue
		}
		var rc winRect
		procGetWindowRect.Call(mark, uintptr(unsafe.Pointer(&rc)))
		procMapPoints.Call(0, hwnd, uintptr(unsafe.Pointer(&rc)), 2)
		y := rc.top + (rc.bottom-rc.top-32)/2
		procDrawIconEx.Call(hdc, uintptr(rc.left-brandGap), uintptr(y), icon, 32, 32, 0, 0, 3)
	}
}

func dotColor(state string) uintptr {
	switch {
	case strings.HasPrefix(state, "Подключено"):
		return rgb(0x1f, 0x6b, 0x4a)
	case strings.HasPrefix(state, "Ошибка"), strings.Contains(state, "Нет связи"):
		return rgb(0xb4, 0x45, 0x38)
	default:
		return rgb(0xc2, 0x8a, 0x2c)
	}
}

func centerOver(owner uintptr, w, h int32) (int32, int32, bool) {
	var rc winRect
	if owner != 0 {
		procGetWindowRect.Call(owner, uintptr(unsafe.Pointer(&rc)))
	} else {
		sw, _, _ := procSystemMetrics.Call(0)
		sh, _, _ := procSystemMetrics.Call(1)
		rc = winRect{0, 0, int32(sw), int32(sh)}
	}
	if rc.right-rc.left < 100 || rc.bottom-rc.top < 100 {
		return 0, 0, false
	}
	x := rc.left + (rc.right-rc.left-w)/2
	y := rc.top + (rc.bottom-rc.top-h)/2
	if y < rc.top {
		y = rc.top
	}
	return x, y, true
}

func control(parent uintptr, className, text string, x, y, w, h int, style uintptr, id uintptr, font uintptr) uintptr {
	classPtr, _ := syscall.UTF16PtrFromString(className)

	shown := text
	if className != "EDIT" {
		shown = tr(text)
	}
	textPtr, _ := syscall.UTF16PtrFromString(shown)
	hwnd, _, _ := procCreateWindow.Call(
		0,
		uintptr(unsafe.Pointer(classPtr)),
		uintptr(unsafe.Pointer(textPtr)),
		style,
		uintptr(x), uintptr(y), uintptr(w), uintptr(h),
		parent, id, 0, 0,
	)
	if hwnd != 0 {
		if className == "EDIT" {
			delete(uiSource, hwnd)
		} else {
			uiSource[hwnd] = text
		}
	}
	if hwnd != 0 && font != 0 {
		procSendMessage.Call(hwnd, wmSetFont, font, 1)
	}
	return hwnd
}

const langW = 108

func langLabel() string {
	if currentLang() == langEN {
		return "Русский"
	}
	return "English"
}

func langButton(parent uintptr, right, y, h int) uintptr {
	var rc winRect
	procGetClientRect.Call(parent, uintptr(unsafe.Pointer(&rc)))
	return admButton(parent, langLabel(), int(rc.right)-right-langW, y, langW, h, idLang)
}

func toggleLang() {
	next := langEN
	if currentLang() == langEN {
		next = langRU
	}
	setLang(next)
	if err := saveLang(next); err != nil {
		log.Printf("язык не сохранён: %v", err)
	}
	btnLabel[idLang] = langLabel()

	self, _, _ := procCurrentThread.Call()
	for hwnd, src := range uiSource {
		if owner, _, _ := procWindowThread.Call(hwnd, 0); owner != self {
			delete(uiSource, hwnd)
			continue
		}
		if _, isField := fields[hwnd]; isField {
			continue
		}
		ptr, _ := syscall.UTF16PtrFromString(tr(src))
		procSetText.Call(hwnd, uintptr(unsafe.Pointer(ptr)))
	}
	for _, hwnd := range []uintptr{pwdHwnd, mainHwnd, admHwnd, dlg.hwnd} {
		if hwnd != 0 {
			procRedrawWindow.Call(hwnd, 0, 0, 0x0001|0x0004|0x0080|0x0100)
		}
	}
}

func sourceText(hwnd uintptr) string {
	if src, ok := uiSource[hwnd]; ok {
		return src
	}
	return windowText(hwnd)
}

func stockFont() uintptr {
	font, _, _ := procStockObject.Call(17)
	return font
}

func show(hwnd uintptr) {
	procShowWindow.Call(hwnd, 5)
	procUpdateWindow.Call(hwnd)
	procSetForeground.Call(hwnd)
}

func messageLoop() {
	var m winMsg
	for {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslate.Call(uintptr(unsafe.Pointer(&m)))
		procDispatch.Call(uintptr(unsafe.Pointer(&m)))
		flushUI()
	}
}

func flushUI() {
	if atomic.SwapUint32(&uiLangFlip, 0) == 1 {
		toggleLang()
	}
	if atomic.SwapUint32(&pwdClick, 0) == 1 {
		acceptPassword()
	}
	if atomic.SwapUint32(&pwdClose, 0) == 1 && pwdHwnd != 0 {
		procDestroyWindow.Call(pwdHwnd)
	}
	if atomic.SwapUint32(&mainClose, 0) == 1 && mainHwnd != 0 {
		if guiCancel != nil {
			guiCancel()
		}
		procDestroyWindow.Call(mainHwnd)
	}
	if atomic.SwapUint32(&uiMainLayout, 0) == 1 && mainHwnd != 0 {
		layoutMain(mainHwnd)
	}
	if cmd := atomic.SwapUintptr(&uiClientCmd, 0); cmd != 0 {
		runClientCmd(cmd)
	}
	if atomic.SwapUint32(&uiStatus, 0) == 1 {
		refreshStatus()
	}
	flushFields()
	flushAdminUI()
}

func flushFields() {
	if edit := atomic.SwapUintptr(&focusWant, 0); edit != 0 {
		if _, ok := fields[edit]; ok {
			procSetFocus.Call(edit)
		}
	}
	if atomic.SwapUint32(&uiFields, 0) != 1 {
		return
	}
	for edit, f := range fields {
		frame := f.frame
		frame.left -= 2
		frame.top -= 2
		frame.right += 2
		frame.bottom += 2
		procInvalidateRect.Call(f.parent, uintptr(unsafe.Pointer(&frame)), 1)
		if f.hint != "" {
			procInvalidateRect.Call(edit, 0, 1)
		}
	}
}

func runClientCmd(cmd uintptr) {
	switch cmd {
	case idCopyMyIP:
		if myIP == "" {
			setClientNote("адрес ещё не выдан")
			return
		}
		copyClientIP(myIP)
	case idClientList:
		i := cur(hwndList)
		if i < 0 || i >= len(clientIPs) || clientIPs[i] == "" {
			return
		}
		copyClientIP(clientIPs[i])
	}
}

func copyClientIP(ip string) {
	if err := copyText(ip); err != nil {
		setClientNote(err.Error())
		return
	}
	setClientNote(ip + " скопирован")
}

func setClientNote(text string) {
	clientNote = text
	clientNoteAt = time.Now()
	guiSig = ""
	atomic.StoreUint32(&uiStatus, 1)
}

func acceptPassword() {
	if pwdHwnd == 0 {
		return
	}
	pwdValue = windowText(pwdEdit)
	if pwdValue == "" {
		setText(pwdHint, "Введите пароль")
		return
	}
	pwdOK = true
	hwnd := pwdHwnd
	pwdHwnd = 0
	procDestroyWindow.Call(hwnd)
}

func pwdProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if r, ok := themeProc(hwnd, msg, wparam, lparam); ok {
		return r
	}
	switch msg {
	case wmCommand:
		if wparam&0xffff == idOK && wparam>>16 == 0 {
			atomic.StoreUint32(&pwdClick, 1)
			return 0
		}
	case wmClose:
		atomic.StoreUint32(&pwdClose, 1)
		return 0
	case wmDestroy:
		pwdHwnd = 0
		procPostQuit.Call(0)
		return 0
	}
	r, _, _ := procDefWindow.Call(hwnd, msg, wparam, lparam)
	return r
}

func layoutMain(hwnd uintptr) {
	var rc winRect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	width := int(rc.right)
	height := int(rc.bottom)
	if width < 320 || height < 280 {
		return
	}

	const margin, cardPad = 28, 20
	inner := width - margin*2
	moveCtl(hwndMark, margin+brandGap, 18, inner-brandGap-langW-12, 34)
	moveCtl(hwndLang, margin+inner-langW, 20, langW, 30)
	stateDot = winRect{int32(margin + 2), 67, int32(margin + 12), 77}
	moveCtl(hwndState, margin+18, 60, inner-18, 24)

	ipCard = winRect{int32(margin), 98, int32(margin + inner), 188}
	copyW := 170
	copyX := margin + inner - cardPad - copyW
	textW := copyX - 12 - (margin + cardPad)
	moveCtl(hwndNet, margin+cardPad, 112, textW, 22)
	moveCtl(hwndIP, margin+cardPad, 136, textW, 38)
	moveCtl(hwndCopyIP, copyX, 98+(90-40)/2, copyW, 40)

	moveCtl(hwndPeers, margin, 206, inner, 22)
	btnY := height - margin - 40
	listY := 232
	listH := btnY - 16 - listY
	if listH < 80 {
		listH = 80
	}
	mainCard = winRect{int32(margin), int32(listY), int32(margin + inner), int32(listY + listH)}
	paintMain = true
	moveCtl(hwndList, margin+1, listY+1, inner-2, listH-2)
	moveCtl(hwndExit, margin, btnY, 150, 40)
	invalidate(hwnd)
}

func setCtlFont(hwnd, font uintptr) {
	if hwnd != 0 && font != 0 {
		procSendMessage.Call(hwnd, wmSetFont, font, 1)
	}
}

type field struct {
	parent uintptr
	edit   uintptr
	frame  winRect
	hint   string
	filter bool
}

func makeField(parent uintptr, text string, x, y, w, h int, password bool, id uintptr, hint string) uintptr {
	style := uintptr(wsChild | wsVisible | esAutoHScroll)
	if password {
		style |= esPassword
	}
	edit := control(parent, "EDIT", text, x, y, w, h, style, id, admFont)
	if edit == 0 {
		return 0
	}
	fields[edit] = &field{parent: parent, edit: edit, hint: hint, filter: id == idAdmFilter}
	cb := syscall.NewCallback(fieldProc)
	keepProc = append(keepProc, cb)
	if prev, _, _ := procSetLongPtr.Call(edit, gwlpWndProc, cb); prev != 0 {
		editOrig[edit] = prev
	}
	placeField(edit, x, y, w, h)
	return edit
}

func placeField(edit uintptr, x, y, w, h int) {
	f := fields[edit]
	if f == nil || w < 40 || h < 20 {
		return
	}
	f.frame = winRect{int32(x), int32(y), int32(x + w), int32(y + h)}
	line := int(lineHeight()) + 2
	if line > h-4 {
		line = h - 4
	}
	moveCtl(edit, x+12, y+(h-line)/2, w-24, line)
}

func lineHeight() int32 {
	if fieldLine != 0 {
		return fieldLine
	}
	ensureUI()
	hdc, _, _ := procGetDC.Call(0)
	if hdc == 0 {
		return 21
	}
	defer procReleaseDC.Call(0, hdc)
	old, _, _ := procSelectObject.Call(hdc, admFont)
	sample, _ := syscall.UTF16PtrFromString("Ag")
	var sz struct{ cx, cy int32 }
	procTextExtent.Call(hdc, uintptr(unsafe.Pointer(sample)), 2, uintptr(unsafe.Pointer(&sz)))
	if old != 0 {
		procSelectObject.Call(hdc, old)
	}
	if sz.cy < 12 {
		return 21
	}
	fieldLine = sz.cy
	return fieldLine
}

func paintFields(hwnd, hdc uintptr) {
	focus, _, _ := procGetFocus.Call()
	for edit, f := range fields {
		if f.parent != hwnd {
			continue
		}
		edge := rgb(0xd5, 0xce, 0xc2)
		if focus == edit {
			edge = rgb(0x1f, 0x6b, 0x4a)
		}
		fillRound(hdc, f.frame, rgb(0xff, 0xff, 0xff), edge, 12)
	}
}

func fieldAt(hwnd uintptr, lparam uintptr) uintptr {
	x := int32(int16(lparam & 0xffff))
	y := int32(int16((lparam >> 16) & 0xffff))
	for edit, f := range fields {
		if f.parent == hwnd && x >= f.frame.left && x < f.frame.right && y >= f.frame.top && y < f.frame.bottom {
			return edit
		}
	}
	return 0
}

func fieldProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	f := fields[hwnd]
	switch msg {
	case wmKeyDown:
		switch {
		case wparam == vkReturn && f != nil && !f.filter:
			procPostMessage.Call(f.parent, wmCommand, uintptr(idOK), 0)
			return 0
		case wparam == vkReturn:
			return 0
		case wparam == vkEscape && f != nil && f.filter:
			atomic.StoreUint32(&clearFilter, 1)
			procPostMessage.Call(f.parent, wmPoke, 0, 0)
			return 0
		case wparam == vkEscape && f != nil:
			procPostMessage.Call(f.parent, wmClose, 0, 0)
			return 0
		}
	case wmChar:
		if wparam == vkReturn || wparam == 10 || wparam == vkEscape {
			return 0
		}
	}
	prev := editOrig[hwnd]
	var r uintptr
	if prev == 0 {
		r, _, _ = procDefWindow.Call(hwnd, msg, wparam, lparam)
	} else {
		r, _, _ = procCallWndProc.Call(prev, hwnd, msg, wparam, lparam)
	}
	switch msg {
	case wmPaint:
		if f != nil && f.hint != "" {
			paintHint(hwnd, f.hint)
		}
	case wmNcDestroy:
		delete(fields, hwnd)
		delete(editOrig, hwnd)
	}
	return r
}

func paintHint(edit uintptr, hint string) {
	if n, _, _ := procTextLength.Call(edit); n != 0 {
		return
	}
	hdc, _, _ := procGetDC.Call(edit)
	if hdc == 0 {
		return
	}
	defer procReleaseDC.Call(edit, hdc)
	var rc winRect
	procGetClientRect.Call(edit, uintptr(unsafe.Pointer(&rc)))
	rc.left += 2
	old, _, _ := procSelectObject.Call(hdc, admFont)
	procSetBkMode.Call(hdc, 1)
	procSetTextColor.Call(hdc, rgb(0x9a, 0x94, 0x88))
	drawUTF(hdc, tr(hint), rc, 0x8824)
	if old != 0 {
		procSelectObject.Call(hdc, old)
	}
}

func invalidate(hwnd uintptr) {
	if hwnd != 0 {
		procInvalidateRect.Call(hwnd, 0, 1)
	}
}

func mainProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if r, ok := themeProc(hwnd, msg, wparam, lparam); ok {
		return r
	}
	switch msg {
	case wmGetMinMax:
		if lparam != 0 {
			info := (*minMaxInfo)(unsafe.Pointer(lparam))
			info.minTrack.x = 560
			info.minTrack.y = 560
		}
		return 0
	case wmSize:
		if wparam != 1 {
			atomic.StoreUint32(&uiMainLayout, 1)
			procPostMessage.Call(hwnd, wmPoke, 0, 0)
		}
		return 0
	case wmTimer:
		atomic.StoreUint32(&uiStatus, 1)
		return 0
	case wmCommand:
		id := wparam & 0xffff
		code := wparam >> 16
		if id == idExit && code == 0 {
			atomic.StoreUint32(&mainClose, 1)
			return 0
		}
		if id == idCopyMyIP && code == 0 {
			atomic.StoreUintptr(&uiClientCmd, idCopyMyIP)
			return 0
		}
		if id == idClientList && code == lbnDblClk {
			atomic.StoreUintptr(&uiClientCmd, idClientList)
			return 0
		}
	case wmClose:
		atomic.StoreUint32(&mainClose, 1)
		return 0
	case wmDestroy:
		procPostQuit.Call(0)
		return 0
	}
	r, _, _ := procDefWindow.Call(hwnd, msg, wparam, lparam)
	return r
}

func refreshStatus() {
	if hwndList == 0 {
		return
	}
	var st vpn.ClientStatus
	switch {
	case statusSource != nil:
		st = statusSource()
	case guiClient != nil:
		st = guiClient.Status()
	default:
		return
	}
	text := st.State
	if st.Detail != "" && st.State != "Подключено" {
		text = st.State + ": " + st.Detail
	}
	ip := "ещё не выдан"
	if st.VirtualIP != "" {
		ip = st.VirtualIP
	}
	net := "Ваш адрес в сети " + st.Network
	lines := make([]string, 0, len(st.Peers)+1)
	ips := make([]string, 0, len(st.Peers)+1)
	if st.VirtualIP != "" {
		who := st.Hostname
		if who == "" {
			who = "этот компьютер"
		}
		lines = append(lines, fmt.Sprintf("%s      %s      это вы", st.VirtualIP, who))
		ips = append(ips, st.VirtualIP)
		if len(st.Peers) == 0 {
			lines = append(lines, "Других участников пока нет")
			ips = append(ips, "")
		}
	}
	if len(st.Peers) == 0 && st.VirtualIP == "" {
		lines = append(lines, "Пока никого нет")
		ips = append(ips, "")
	}
	for _, peer := range st.Peers {
		mode := "через сервер"
		if peer.Direct {
			mode = "напрямую"
		}
		host := peer.Hostname
		if host == "" {
			host = "без имени"
		}
		lines = append(lines, fmt.Sprintf("%s      %s      %s", peer.IP, host, mode))
		ips = append(ips, peer.IP)
	}
	title := "Участники"
	if len(st.Peers) > 0 {
		title = fmt.Sprintf("Участники · %d", len(st.Peers)+1)
	}
	if clientNote != "" && time.Since(clientNoteAt) < 4*time.Second {
		title += " · " + clientNote
	} else {
		clientNote = ""
	}
	myIP = st.VirtualIP
	sig := text + "\n" + ip + "\n" + net + "\n" + title + "\n" + fmt.Sprint(lines)
	if sig == guiSig {
		return
	}
	guiSig = sig
	if sourceText(hwndState) != text {
		dot := stateDot
		procInvalidateRect.Call(mainHwnd, uintptr(unsafe.Pointer(&dot)), 1)
	}
	setText(hwndState, text)
	setText(hwndIP, ip)
	setText(hwndNet, net)
	setText(hwndPeers, title)
	prev := cur(hwndList)
	procSendMessage.Call(hwndList, lbResetContent, 0, 0)
	for _, line := range lines {
		addList(line)
	}
	clientIPs = ips
	if prev >= 0 && prev < len(lines) {
		choose(hwndList, prev)
	}
}

func addList(text string) {
	ptr, _ := syscall.UTF16PtrFromString(text)
	procSendMessage.Call(hwndList, lbAddString, 0, uintptr(unsafe.Pointer(ptr)))
}

func showError(err error) {
	if err == nil {
		return
	}
	log.Println(err)
	messageBox(err.Error())
}

func messageBox(text string) {
	body, _ := syscall.UTF16PtrFromString(tr(text))
	title, _ := syscall.UTF16PtrFromString("Lokalka")
	procMessageBox.Call(0, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x10)
}

func confirmBox(text string) bool {
	return confirmOwned(admHwnd, text)
}

func confirmOwned(owner uintptr, text string) bool {
	body, _ := syscall.UTF16PtrFromString(tr(text))
	title, _ := syscall.UTF16PtrFromString("Lokalka")
	r, _, _ := procMessageBox.Call(owner, uintptr(unsafe.Pointer(body)), uintptr(unsafe.Pointer(title)), 0x24)
	return r == 6
}

func windowText(hwnd uintptr) string {
	if hwnd == 0 {
		return ""
	}
	buf := make([]uint16, 512)
	procGetText.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func setText(hwnd uintptr, text string) {
	if hwnd == 0 {
		return
	}
	if _, isField := fields[hwnd]; !isField {
		uiSource[hwnd] = text
		text = tr(text)
	}
	ptr, _ := syscall.UTF16PtrFromString(text)
	procSetText.Call(hwnd, uintptr(unsafe.Pointer(ptr)))
}

func copyText(text string) error {
	utf16, err := syscall.UTF16FromString(text)
	if err != nil {
		return err
	}
	bytes := len(utf16) * 2
	r, _, _ := procOpenClipboard.Call(0)
	if r == 0 {
		return errors.New("не удалось открыть буфер обмена")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	mem, _, _ := procGlobalAlloc.Call(0x0002, uintptr(bytes))
	if mem == 0 {
		return errors.New("не хватило памяти для буфера обмена")
	}
	ptr, _, _ := procGlobalLock.Call(mem)
	if ptr == 0 {
		return errors.New("не удалось записать в буфер обмена")
	}
	copy((*[1 << 20]uint16)(unsafe.Pointer(ptr))[:len(utf16):len(utf16)], utf16)
	procGlobalUnlock.Call(mem)
	if r, _, _ := procSetClipboardData.Call(13, mem); r == 0 {
		return errors.New("не удалось положить текст в буфер обмена")
	}
	return nil
}
