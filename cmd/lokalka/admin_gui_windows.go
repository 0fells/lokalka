//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"lokalka/internal/proto"
	"lokalka/internal/stamp"
	"lokalka/internal/vpn"
)

const (
	lbGetCurSel      = 0x0188
	lbSetCurSel      = 0x0186
	idAdmUpdate      = 10
	idAdmNewNet      = 11
	idAdmDelNet      = 12
	idAdmPass        = 13
	idAdmNewCfg      = 14
	idAdmSave        = 15
	idAdmDelCfg      = 16
	idAdmKick        = 17
	idAdmLinux       = 18
	idAdmAllow       = 19
	idAdmKickAll     = 20
	idAdmCopyPass    = 21
	idAdmRename      = 22
	idAdmCopyIP      = 23
	idNetList        = 30
	idPeerList       = 31
	idCfgList        = 32
	idBlockList      = 33
	idAdmFilter      = 34
	idPanelList      = 35
	idAdmTabFiles    = 24
	idAdmTabPanels   = 25
	idAdmNewPanel    = 26
	idAdmResetPanel  = 27
	idAdmTogglePanel = 28
	idAdmDelPanel    = 29
	idClientList     = 40
	idLang           = 41
	lbnSelChange     = 1
	lbnDblClk        = 2
	enChange         = 0x0300
	wmAdm            = 0x8000
	wmPoke           = 0x8001
	wmSize           = 0x0005
	wmGetMinMax      = 0x0024
	wmDrawItem       = 0x002B
	wmMeasure        = 0x002C
	wmErase          = 0x0014
	wmCtlStatic      = 0x0138
	wmCtlEdit        = 0x0133
	wmCtlList        = 0x0134
	bsOwnerDraw      = 0x000B
	dtCenter         = 0x0025
	odsSelected      = 0x0001
	esAutoHScroll    = 0x0080
	maxConfigsUI     = 20
)

type dialogState struct {
	done         bool
	ok           bool
	needB        bool
	login        bool
	dialing      bool
	hwnd         uintptr
	editA, editB uintptr
	hint         uintptr
	owner        uintptr
	a, b         string

	pin       []byte
	pinServer string
}

type drawItem struct {
	ctlType    uint32
	ctlID      uint32
	itemID     uint32
	itemAction uint32
	itemState  uint32
	hwndItem   uintptr
	hdc        uintptr
	left       int32
	top        int32
	right      int32
	bottom     int32
	itemData   uintptr
}

type winRect struct {
	left, top, right, bottom int32
}

var (
	admSess           *vpn.AdminSession
	admNetList        uintptr
	admPeerList       uintptr
	admBlockList      uintptr
	admCfgList        uintptr
	admFilter         uintptr
	admNetNames       []string
	admPeerIPs        []string
	admBlockHosts     []string
	admCfgIDs         []string
	admCfgLabels      []string
	admFilling        bool
	admPanelList      uintptr
	admPanelIDs       []string
	admPanelNames     []string
	admPanelOff       []bool
	admTabPanels      bool
	admBtnTabFiles    uintptr
	admBtnTabPanels   uintptr
	admBtnNewPanel    uintptr
	admBtnResetPanel  uintptr
	admBtnTogglePanel uintptr
	admBtnDelPanel    uintptr

	admLimited    bool
	admOwner      string
	admDenied     bool
	admStatus     uintptr
	admMark       uintptr
	admRefresh    uintptr
	admLang       uintptr
	admTitleNet   uintptr
	admTitlePeer  uintptr
	admTitleBlock uintptr
	admTitleCfg   uintptr
	admBtnNewNet  uintptr
	admBtnDelNet  uintptr
	admBtnPass    uintptr
	admBtnCopy    uintptr
	admBtnKick    uintptr
	admBtnKickAll uintptr
	admBtnAllow   uintptr
	admBtnNewCfg  uintptr
	admBtnSave    uintptr
	admBtnLinux   uintptr
	admBtnDelCfg  uintptr
	admBtnRename  uintptr
	admFooter     uintptr
	admHwnd       uintptr
	admMu         sync.Mutex
	admBusy       bool
	admListing    bool
	admPending    bool
	listGen       int
	admListed     bool
	admSig        string
	admNets       []proto.AdminNet
	admErr        error
	admNote       string
	admNoteAt     time.Time
	admListedAt   time.Time
	dlg           dialogState
	loginSess     *vpn.AdminSession
	loginErr      error
	loginProbe    []byte
	dialGen       int
	errAbort      = errors.New("закрыто")

	uiFetch      uint32
	uiApply      uint32
	uiSel        uint32
	uiLayout     uint32
	uiFilter     uint32
	uiPass       uint32
	uiCmd        uintptr
	formClick    uint32
	formClose    uint32
	loginReady   uint32
	admClose     uint32
	flushDepth   int
	admFont      uintptr
	admFontSemi  uintptr
	admFontTitle uintptr
	bgBrush      uintptr
	listBrush    uintptr
	cardBrush    uintptr
	btnLabel     = map[uintptr]string{}
	admCards     [3]winRect
	paintCards   bool
	mainCard     winRect
	paintMain    bool
	statusTone   uintptr
	loginLead    uintptr
)

func runAdminGUI() error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sess, err := loginAdmin()
	if err != nil {
		return nil
	}
	defer sess.Close()
	admSess = sess
	err = adminWindow()
	admSess = nil
	return err
}

func runPanelGUI(file stamp.File) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	admLimited = true
	admOwner = strings.TrimSpace(file.Label)
	pin, err := vpn.ParseServerKey(file.ServerKey)
	if err != nil {
		return err
	}
	sess := vpn.NewPanelSession(file.Server, file.Network, file.AdminID, file.Password, pin)
	defer sess.Close()
	admSess = sess
	err = adminWindow()
	admSess = nil
	return err
}

func loginAdmin() (*vpn.AdminSession, error) {
	admMu.Lock()
	dialGen++
	loginSess = nil
	loginErr = nil
	admMu.Unlock()
	resetFormFlags()
	dlg = dialogState{needB: true, login: true}
	hwnd := newWindow("LokalkaForm", formProc, "Lokalka · управление", 480, 440, false, 0)
	if hwnd == 0 {
		return nil, errAbort
	}
	dlg.hwnd = hwnd
	brandMark(hwnd, 32, 24, 400-langW-12, 34)
	langButton(hwnd, 32, 26, 30)
	loginLead = control(hwnd, "STATIC", "Управление сетями", 32+brandGap, 58, 400-brandGap, 22, wsChild|wsVisible, 0, admFont)
	control(hwnd, "STATIC", "Адрес сервера", 32, 98, 400, 22, wsChild|wsVisible, 0, admFont)
	server := adminServerPreset()
	dlg.editA = makeField(hwnd, server, 32, 122, 400, 40, false, 0, "")
	control(hwnd, "STATIC", "Пароль админки", 32, 176, 400, 22, wsChild|wsVisible, 0, admFont)
	dlg.editB = makeField(hwnd, "", 32, 200, 400, 40, true, 0, "")
	dlg.hint = control(hwnd, "STATIC", "", 32, 250, 400, 40, wsChild|wsVisible, 0, admFont)
	admButton(hwnd, "Войти", 32, 298, 160, 42, idOK)
	show(hwnd)
	if strings.TrimSpace(server) != "" {
		procSetFocus.Call(dlg.editB)
	} else {
		procSetFocus.Call(dlg.editA)
	}
	dialogLoop()
	loginLead = 0
	admMu.Lock()
	sess := loginSess
	loginSess = nil
	admMu.Unlock()
	if !dlg.ok || sess == nil {
		if sess != nil {
			sess.Close()
		}
		return nil, errAbort
	}
	return sess, nil
}

func adminWindow() error {
	resetFormFlags()
	atomic.StoreUint32(&admClose, 0)
	atomic.StoreUint32(&uiFetch, 0)
	atomic.StoreUint32(&uiApply, 0)
	admListed = false
	admSig = ""
	title := "Lokalka"
	if admLimited {
		title = "Lokalka · панель"
		if admOwner != "" {
			title += " " + admOwner
		}
	}
	hwnd := newWindow("LokalkaAdmin", adminProc, title, 1120, 760, true, 0)
	if hwnd == 0 {
		return fmt.Errorf("не удалось открыть окно")
	}
	admTabPanels = false
	admDenied = false
	admMark = brandMark(hwnd, 20, 14, 110+brandGap, 36)
	admStatus = control(hwnd, "STATIC", "Загрузка...", 198, 22, 500, 24, wsChild|wsVisible|0x4000, 0, admFont)
	admRefresh = admButton(hwnd, "Обновить", 848, 16, 148, 36, idAdmUpdate)
	admLang = admButton(hwnd, langLabel(), 730, 16, langW, 36, idLang)
	admTitleNet = control(hwnd, "STATIC", "Сети", 32, 82, 280, 22, wsChild|wsVisible, 0, admFontSemi)
	admTitlePeer = control(hwnd, "STATIC", "Кто подключён", 360, 82, 280, 22, wsChild|wsVisible, 0, admFontSemi)
	admTitleBlock = control(hwnd, "STATIC", "Отключены", 360, 320, 280, 22, wsChild|wsVisible, 0, admFontSemi)
	admTitleCfg = control(hwnd, "STATIC", "Файлы для входа", 688, 82, 280, 22, wsChild|wsVisible, 0, admFontSemi)
	setCtlFont(admTitleNet, admFontSemi)
	setCtlFont(admTitlePeer, admFontSemi)
	setCtlFont(admTitleBlock, admFontSemi)
	setCtlFont(admTitleCfg, admFontSemi)
	admFilter = makeField(hwnd, "", 360, 108, 280, 38, false, idAdmFilter, "Поиск по IP или имени")
	admNetList = control(hwnd, "LISTBOX", "", 32, 112, 280, 360, prettyList, idNetList, admFont)
	admPeerList = control(hwnd, "LISTBOX", "", 360, 168, 280, 160, prettyList, idPeerList, admFont)
	admBlockList = control(hwnd, "LISTBOX", "", 360, 348, 280, 100, prettyList, idBlockList, admFont)
	admCfgList = control(hwnd, "LISTBOX", "", 688, 112, 280, 360, prettyList, idCfgList, admFont)
	admBtnNewNet = admButton(hwnd, "Создать сеть", 32, 440, 134, 40, idAdmNewNet)
	admBtnDelNet = admButton(hwnd, "Удалить сеть", 176, 440, 136, 40, idAdmDelNet)
	admBtnPass = admButton(hwnd, "Новый пароль сети", 32, 488, 280, 40, idAdmPass)
	admBtnCopy = admButton(hwnd, "Скопировать пароль", 32, 536, 280, 40, idAdmCopyPass)
	admBtnKick = admButton(hwnd, "Отключить", 360, 488, 280, 40, idAdmKick)
	admBtnKickAll = admButton(hwnd, "Всех", 360, 536, 134, 40, idAdmKickAll)
	admBtnAllow = admButton(hwnd, "Разрешить", 504, 536, 136, 40, idAdmAllow)
	admBtnNewCfg = admButton(hwnd, "Новый файл", 688, 440, 134, 40, idAdmNewCfg)
	admBtnSave = admButton(hwnd, "Скачать exe", 832, 440, 136, 40, idAdmSave)
	admBtnLinux = admButton(hwnd, "Скачать Linux", 688, 488, 134, 40, idAdmLinux)
	admBtnDelCfg = admButton(hwnd, "Удалить файл", 832, 488, 136, 40, idAdmDelCfg)
	admBtnRename = admButton(hwnd, "Переименовать файл", 688, 536, 280, 40, idAdmRename)
	admBtnTabFiles = admButton(hwnd, "Файлы", 688, 80, 134, 30, idAdmTabFiles)
	admBtnTabPanels = admButton(hwnd, "Панели", 832, 80, 136, 30, idAdmTabPanels)
	admPanelList = control(hwnd, "LISTBOX", "", 688, 112, 280, 360, prettyList, idPanelList, admFont)
	admBtnNewPanel = admButton(hwnd, "Создать панель", 688, 440, 134, 40, idAdmNewPanel)
	admBtnResetPanel = admButton(hwnd, "Новый ключ", 832, 440, 136, 40, idAdmResetPanel)
	admBtnTogglePanel = admButton(hwnd, "Приостановить", 688, 488, 134, 40, idAdmTogglePanel)
	admBtnDelPanel = admButton(hwnd, "Удалить панель", 832, 488, 136, 40, idAdmDelPanel)
	footer := "Файлы сохраняются в папку download. Двойной щелчок по файлу скачивает exe, по участнику копирует IP."
	if admLimited {
		footer = "Загрузка..."
	}
	admFooter = control(hwnd, "STATIC", footer, 20, 640, 1000, 24, wsChild|wsVisible|0x4000, 0, admFont)
	admHwnd = hwnd
	if !admLimited {
		procShowWindow.Call(admTitleCfg, 0)
	}
	if admLimited {
		for _, c := range []uintptr{admTitleNet, admNetList, admBtnNewNet, admBtnDelNet, admBtnPass, admBtnCopy,
			admBtnLinux, admBtnTabFiles, admBtnTabPanels} {
			procShowWindow.Call(c, 0)
		}
	}
	showTab(false)
	layoutAdmin(hwnd)
	show(hwnd)
	procSetTimer.Call(hwnd, 1, 5000, 0)
	atomic.StoreUint32(&uiFetch, 1)
	procPostMessage.Call(hwnd, wmAdm, 0, 0)
	messageLoop()
	admHwnd = 0
	return nil
}

func layoutAdmin(hwnd uintptr) {
	var rc winRect
	procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
	width := int(rc.right)
	height := int(rc.bottom)
	if width < 640 || height < 420 {
		return
	}
	const margin, gap = 20, 14
	colW := (width - margin*2 - gap*2) / 3
	x0 := margin
	x1 := x0 + colW + gap
	x2 := x1 + colW + gap
	if admLimited {
		colW = (width - margin*2 - gap) / 2
		x1 = x0 + colW + gap
		x2 = x0
	}
	footerY := height - 20 - 28
	cardTop := 68
	cardBottom := footerY - 16
	pad := 14
	titleY := cardTop + 14
	listY := titleY + 28
	btn3 := cardBottom - pad - 40
	btn2 := btn3 - 8 - 40
	btn1 := btn2 - 8 - 40
	listBottom := btn1 - 10
	if listBottom-listY < 80 {
		listBottom = listY + 80
	}
	admCards[0] = winRect{int32(x0), int32(cardTop), int32(x0 + colW), int32(cardBottom)}
	admCards[1] = winRect{int32(x1), int32(cardTop), int32(x1 + colW), int32(cardBottom)}
	admCards[2] = winRect{int32(x2), int32(cardTop), int32(x2 + colW), int32(cardBottom)}
	if admLimited {
		admCards[0] = winRect{int32(x0), int32(cardTop), int32(x0 + colW), int32(cardBottom)}
		admCards[1] = winRect{int32(x1), int32(cardTop), int32(x1 + colW), int32(cardBottom)}
		admCards[2] = winRect{}
	}
	paintCards = true
	refreshW := 148
	refreshX := width - margin - refreshW
	langX := refreshX - 10 - langW
	moveCtl(admMark, margin+brandGap, 16, 110, 36)
	statusX := margin + brandGap + 126
	moveCtl(admStatus, statusX, 24, langX-statusX-12, 24)
	moveCtl(admLang, langX, 16, langW, 36)
	moveCtl(admRefresh, refreshX, 16, refreshW, 36)
	placeColumn(0, x0, colW, pad, titleY, listY, listBottom, btn1, btn2, btn3)
	placeColumn(1, x1, colW, pad, titleY, listY, listBottom, btn1, btn2, btn3)
	placeColumn(2, x2, colW, pad, titleY, listY, listBottom, btn1, btn2, btn3)
	moveCtl(admFooter, margin, footerY, width-margin*2, 24)

	procRedrawWindow.Call(hwnd, 0, 0, 0x0001|0x0004|0x0080|0x0100)
}

func placeColumn(n, x, colW, pad, titleY, listY, listBottom, btn1, btn2, btn3 int) {
	innerX := x + pad
	innerW := colW - pad*2
	if innerW < 80 {
		innerW = 80
	}
	half := (innerW - 10) / 2
	listH := listBottom - listY
	if listH < 80 {
		listH = 80
	}
	switch n {
	case 0:
		moveCtl(admTitleNet, innerX, titleY, innerW, 22)
		moveCtl(admNetList, innerX, listY, innerW, listH)
		moveCtl(admBtnNewNet, innerX, btn1, half, 40)
		moveCtl(admBtnDelNet, innerX+half+10, btn1, half, 40)
		moveCtl(admBtnPass, innerX, btn2, innerW, 40)
		moveCtl(admBtnCopy, innerX, btn3, innerW, 40)
	case 1:

		const blockHead = 8 + 24
		filterY := listY
		contentTop := filterY + 38 + 10
		avail := btn2 - 10 - contentTop
		blockH := avail / 3
		if blockH < 60 {
			blockH = 60
		}
		peerH := avail - blockHead - blockH
		if peerH < 60 {
			peerH = 60
		}
		blockY := contentTop + peerH + 8
		moveCtl(admTitlePeer, innerX, titleY, innerW, 22)
		placeField(admFilter, innerX, filterY, innerW, 38)
		moveCtl(admPeerList, innerX, contentTop, innerW, peerH)
		moveCtl(admTitleBlock, innerX, blockY, innerW, 22)
		moveCtl(admBlockList, innerX, blockY+24, innerW, blockH)
		moveCtl(admBtnKick, innerX, btn2, innerW, 40)
		moveCtl(admBtnKickAll, innerX, btn3, half, 40)
		moveCtl(admBtnAllow, innerX+half+10, btn3, half, 40)
	default:

		moveCtl(admTitleCfg, innerX, titleY, innerW, 22)
		moveCtl(admBtnTabFiles, innerX, titleY-2, half, 30)
		moveCtl(admBtnTabPanels, innerX+half+10, titleY-2, half, 30)
		moveCtl(admCfgList, innerX, listY, innerW, listH)
		moveCtl(admBtnNewCfg, innerX, btn1, half, 40)
		moveCtl(admBtnSave, innerX+half+10, btn1, half, 40)
		moveCtl(admBtnLinux, innerX, btn2, half, 40)
		moveCtl(admBtnDelCfg, innerX+half+10, btn2, half, 40)
		moveCtl(admBtnRename, innerX, btn3, innerW, 40)
		if admLimited {
			moveCtl(admBtnRename, innerX, btn2, half, 40)
		}
		moveCtl(admPanelList, innerX, listY, innerW, listH)
		moveCtl(admBtnNewPanel, innerX, btn1, half, 40)
		moveCtl(admBtnResetPanel, innerX+half+10, btn1, half, 40)
		moveCtl(admBtnTogglePanel, innerX, btn2, half, 40)
		moveCtl(admBtnDelPanel, innerX+half+10, btn2, half, 40)
	}
}

func showTab(panels bool) {
	admTabPanels = panels
	files := []uintptr{admCfgList, admBtnNewCfg, admBtnSave, admBtnLinux, admBtnDelCfg, admBtnRename}
	panelCtl := []uintptr{admPanelList, admBtnNewPanel, admBtnResetPanel, admBtnTogglePanel, admBtnDelPanel}
	vis := func(group []uintptr, on bool) {
		cmd := uintptr(0)
		if on {
			cmd = 5
		}
		for _, c := range group {
			procShowWindow.Call(c, cmd)
		}
	}
	if admLimited {
		panels = false
		admTabPanels = false
	}
	vis(files, !panels)
	vis(panelCtl, panels)
	if admLimited {
		procShowWindow.Call(admBtnLinux, 0)
	}
	if !admLimited {
		text := "Файлы сохраняются в папку download. Двойной щелчок по файлу скачивает exe, по участнику копирует IP."
		if panels {
			text = "Панель: отдельный exe с тем же управлением, но только для выбранной сети (файлы и отключения). Ключ показывается один раз; если его потеряли, нажмите \"Новый ключ\"."
		}
		setText(admFooter, text)
	}
	invalidate(admBtnTabFiles)
	invalidate(admBtnTabPanels)
}

func moveCtl(hwnd uintptr, x, y, w, h int) {
	if hwnd == 0 || w < 1 || h < 1 {
		return
	}
	procMoveWindow.Call(hwnd, uintptr(x), uintptr(y), uintptr(w), uintptr(h), 1)
}

type minMaxInfo struct {
	reserved, maxSize, maxPos, minTrack, maxTrack struct{ x, y int32 }
}

func adminProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if r, ok := themeProc(hwnd, msg, wparam, lparam); ok {
		return r
	}
	switch msg {
	case wmGetMinMax:
		if lparam != 0 {
			info := (*minMaxInfo)(unsafe.Pointer(lparam))
			info.minTrack.x = 980
			info.minTrack.y = 720
			if admLimited {
				info.minTrack.x = 860
				info.minTrack.y = 560
			}
		}
		return 0
	case wmSize:
		if wparam != 1 {
			atomic.StoreUint32(&uiLayout, 1)
			procPostMessage.Call(hwnd, wmPoke, 0, 0)
		}
		return 0
	case wmAdm, wmTimer:
		if msg == wmTimer {
			atomic.StoreUint32(&uiFetch, 1)
		}
		return 0
	case wmCommand:
		id := wparam & 0xffff
		code := wparam >> 16
		if code == lbnSelChange && id == idNetList {
			if !admFilling {
				atomic.StoreUint32(&uiSel, 1)
			}
			return 0
		}
		if code == lbnDblClk && id == idCfgList {
			atomic.StoreUintptr(&uiCmd, idAdmSave)
			return 0
		}
		if code == lbnDblClk && id == idPeerList {
			atomic.StoreUintptr(&uiCmd, idAdmCopyIP)
			return 0
		}
		if code == lbnSelChange && id == idPanelList {
			if !admFilling {
				atomic.StoreUint32(&uiFilter, 1)
			}
			return 0
		}
		if code == enChange && id == idAdmFilter {
			atomic.StoreUint32(&uiFilter, 1)
			return 0
		}
		if code != 0 {
			break
		}
		atomic.StoreUintptr(&uiCmd, id)
		return 0
	case wmClose:
		atomic.StoreUint32(&admClose, 1)
		return 0
	case wmDestroy:
		admHwnd = 0
		procPostQuit.Call(0)
		return 0
	}
	r, _, _ := procDefWindow.Call(hwnd, msg, wparam, lparam)
	return r
}

func requestRefresh() {
	if admHwnd == 0 || admSess == nil {
		return
	}
	admMu.Lock()
	if admDenied {
		admMu.Unlock()
		return
	}
	if admListing {
		admPending = true
		admMu.Unlock()
		return
	}
	admListing = true
	admPending = false
	sess := admSess
	gen := listGen
	admMu.Unlock()
	go func() {
		nets, err := sess.List()
		admMu.Lock()
		stale := gen != listGen
		pending := admPending
		admListing = false
		admPending = false
		if !stale {
			if err != nil {
				admErr = err
				if admLimited && errors.Is(err, vpn.ErrDenied) {
					admDenied = true
				}
			} else {
				admErr = nil
				admNets = nets
				admListedAt = time.Now()
			}
		}
		hwnd := admHwnd
		admMu.Unlock()
		if !stale {
			pokeApply(hwnd)
		}
		if pending || stale {
			requestRefresh()
		}
	}()
}

func pokeApply(hwnd uintptr) {
	if hwnd == 0 {
		return
	}
	atomic.StoreUint32(&uiApply, 1)
	procPostMessage.Call(hwnd, wmAdm, 0, 0)
}

func applyAdmin() {
	if admNetList == 0 {
		return
	}
	admMu.Lock()
	nets := append([]proto.AdminNet(nil), admNets...)
	err := admErr
	note := admNote
	if note != "" && time.Since(admNoteAt) > 8*time.Second {
		admNote = ""
		note = ""
	}
	listedAt := admListedAt
	admMu.Unlock()
	if err != nil {
		paintStatus(err.Error())
		return
	}
	status := "На связи с сервером"
	if !listedAt.IsZero() {
		status += " · обновлено " + listedAt.Format("15:04:05")
	}
	if note != "" {
		status = note
	}
	if admLimited && len(nets) > 0 {
		setText(admFooter, fmt.Sprintf("Панель \"%s\" · сеть %s. Двойной щелчок по файлу скачивает exe, по участнику копирует IP.", admOwner, nets[0].Name))
	}
	sig := netsSignature(nets)
	if admListed && sig == admSig {
		showNetwork()
		paintStatus(status)
		return
	}
	prev := selectedNetName()
	admFilling = true
	admNetNames = admNetNames[:0]
	labels := make([]string, 0, len(nets))
	for _, net := range nets {
		admNetNames = append(admNetNames, net.Name)
		state := "никого нет"
		switch {
		case net.Online == 1:
			state = "1 в сети"
		case net.Online > 1:
			state = fmt.Sprintf("%d в сети", net.Online)
		}
		labels = append(labels, net.Name+"      "+state)
	}
	setList(admNetList, labels, "Сетей ещё нет")
	sel := 0
	for i, name := range admNetNames {
		if name == prev {
			sel = i
		}
	}
	if len(admNetNames) > 0 {
		choose(admNetList, sel)
	}
	showNetwork()
	admFilling = false
	admSig = sig
	admListed = true
	paintStatus(status)
}

func showNetwork() {
	if admPeerList == 0 {
		return
	}
	admFilling = true
	defer func() { admFilling = false }()
	prevIP := selectedIP()
	prevHost := selectedBlockedHost()
	prevID := selectedCfgID()
	net, ok := selectedNetwork()
	filter := windowText(admFilter)
	admPeerIPs = admPeerIPs[:0]
	admBlockHosts = admBlockHosts[:0]
	admCfgIDs = admCfgIDs[:0]
	admCfgLabels = admCfgLabels[:0]
	prevPanel := selectedPanelID()
	admPanelIDs = admPanelIDs[:0]
	admPanelNames = admPanelNames[:0]
	admPanelOff = admPanelOff[:0]
	if !ok {
		setText(admTitlePeer, "Кто подключён")
		setText(admTitleBlock, "Отключены")
		setText(admTitleCfg, "Файлы для входа")
		setList(admPeerList, nil, "Выберите сеть слева")
		setList(admBlockList, nil, "Выберите сеть слева")
		setList(admCfgList, nil, "Выберите сеть слева")
		setList(admPanelList, nil, "Выберите сеть слева")
		setTabLabels(0, 0)
		updatePanelButtons()
		return
	}
	setTabLabels(len(net.Configs), len(net.Panels))
	setText(admTitlePeer, fmt.Sprintf("Кто подключён · %d", len(net.Peers)))
	setText(admTitleBlock, fmt.Sprintf("Отключены · %d", len(net.Blocked)))
	setText(admTitleCfg, fmt.Sprintf("Файлы для входа · %d/%d", len(net.Configs), maxConfigsUI))
	peers := make([]string, 0, len(net.Peers))
	peerAt := -1
	for _, peer := range net.Peers {
		who := peer.Hostname
		if who == "" {
			who = "без имени"
		}
		if !matchFilter(filter, peer.IP, who, peer.Endpoint) {
			continue
		}
		if peer.IP == prevIP {
			peerAt = len(admPeerIPs)
		}
		admPeerIPs = append(admPeerIPs, peer.IP)
		peers = append(peers, formatPeerLine(peer))
	}
	emptyPeers := "Сейчас никого нет"
	if len(net.Peers) > 0 && len(peers) == 0 {
		emptyPeers = "Никого по фильтру"
	}
	setList(admPeerList, peers, emptyPeers)
	if peerAt >= 0 {
		choose(admPeerList, peerAt)
	}
	blocked := make([]string, 0, len(net.Blocked))
	blockAt := -1
	for _, peer := range net.Blocked {
		who := peer.Hostname
		if who == "" {
			who = "без имени"
		}
		if !matchFilter(filter, peer.IP, who) {
			continue
		}
		if peer.Hostname == prevHost {
			blockAt = len(admBlockHosts)
		}
		admBlockHosts = append(admBlockHosts, peer.Hostname)
		line := who
		if peer.IP != "" {
			line = peer.IP + "      " + who
		}
		blocked = append(blocked, line)
	}
	emptyBlocked := "Никого не отключали"
	if len(net.Blocked) > 0 && len(blocked) == 0 {
		emptyBlocked = "Никого по фильтру"
	}
	setList(admBlockList, blocked, emptyBlocked)
	if blockAt >= 0 {
		choose(admBlockList, blockAt)
	}
	files := make([]string, 0, len(net.Configs))
	cfgAt := -1
	for _, cfg := range net.Configs {
		if cfg.ID == prevID {
			cfgAt = len(admCfgIDs)
		}
		admCfgIDs = append(admCfgIDs, cfg.ID)
		admCfgLabels = append(admCfgLabels, cfg.Label)
		files = append(files, formatConfigLine(cfg))
	}
	setList(admCfgList, files, "Файлов ещё нет")
	if cfgAt >= 0 {
		choose(admCfgList, cfgAt)
	}
	lines := make([]string, 0, len(net.Panels))
	panelAt := -1
	for _, pn := range net.Panels {
		if pn.ID == prevPanel {
			panelAt = len(admPanelIDs)
		}
		admPanelIDs = append(admPanelIDs, pn.ID)
		admPanelNames = append(admPanelNames, pn.Label)
		admPanelOff = append(admPanelOff, pn.Disabled)
		lines = append(lines, formatPanelLine(pn))
	}
	setList(admPanelList, lines, "Панелей ещё нет")
	if panelAt >= 0 {
		choose(admPanelList, panelAt)
	}
	updatePanelButtons()
}

func formatPanelLine(pn proto.AdminPanel) string {
	parts := []string{pn.Label}
	switch {
	case pn.Disabled:
		parts = append(parts, "", "приостановлена")
	case pn.Online:
		parts = append(parts, "", "в панели сейчас")
	default:
		parts = append(parts, "")
	}
	switch {
	case pn.LastSeen > 0 && !pn.Online:
		parts = append(parts, fmt.Sprintf("вход %v назад", formatAgo(pn.LastSeen)))
		if host := strings.TrimSpace(pn.LastHost); host != "" {
			parts = append(parts, host)
		}
	case pn.LastSeen <= 0:
		parts = append(parts, "ещё не входила")
	}
	return strings.Join(parts, "      ")
}
func setTabLabels(files, panels int) {
	setBtnLabel(idAdmTabFiles, admBtnTabFiles, fmt.Sprintf("Файлы · %d/%d", files, maxConfigsUI))
	setBtnLabel(idAdmTabPanels, admBtnTabPanels, fmt.Sprintf("Панели · %d", panels))
}

func setBtnLabel(id, hwnd uintptr, label string) {
	if btnLabel[id] == label {
		return
	}
	btnLabel[id] = label
	invalidate(hwnd)
}

func updatePanelButtons() {
	label := "Приостановить"
	if i := curPanel(); i >= 0 && i < len(admPanelOff) && admPanelOff[i] {
		label = "Включить"
	}
	setBtnLabel(idAdmTogglePanel, admBtnTogglePanel, label)
}

func curPanel() int { return cur(admPanelList) }

func selectedPanelID() string {
	i := curPanel()
	if i < 0 || i >= len(admPanelIDs) {
		return ""
	}
	return admPanelIDs[i]
}

func formatPeerLine(peer proto.AdminPeer) string {
	who := peer.Hostname
	if who == "" {
		who = "без имени"
	}
	path := "нет UDP"
	if peer.Path == "udp" {
		path = "UDP есть"
	}
	meta := path
	if age := formatAgo(peer.Joined); age != "" {
		meta = path + " · в сети " + age
	}
	where := strings.TrimSpace(peer.Endpoint)
	if where == "" {
		where = "адрес ещё не виден"
	}
	if admLimited {
		where = ""
	}
	cfg := strings.TrimSpace(peer.Config)
	if cfg == "" {
		cfg = "файл неизвестен"
	} else {
		cfg = "файл " + cfg
	}
	if where == "" {
		return peer.IP + "      " + who + "      " + meta + "      " + cfg
	}
	return peer.IP + "      " + who + "      " + meta + "      " + where + "      " + cfg
}

func formatConfigLine(cfg proto.AdminCfg) string {
	if cfg.LastSeen <= 0 {
		return cfg.Label + "      " + "" + "      " + "входов пока не было"
	}
	line := cfg.Label + "      " + "" + "      " + fmt.Sprintf("вход %v назад", formatAgo(cfg.LastSeen))
	if host := strings.TrimSpace(cfg.LastHost); host != "" {
		line += "      " + host
	}
	return line
}

func formatAgo(unix int64) string {
	if unix <= 0 {
		return ""
	}
	d := time.Since(time.Unix(unix, 0))
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "<1 мин"
	case d < time.Hour:
		return fmt.Sprintf("%d мин", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d ч %d мин", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%d дн", int(d.Hours()/24))
	}
}

func matchFilter(filter string, parts ...string) bool {
	filter = strings.ToLower(strings.TrimSpace(filter))
	if filter == "" {
		return true
	}
	for _, part := range parts {
		if strings.Contains(strings.ToLower(part), filter) {
			return true
		}
	}
	return false
}

func setList(list uintptr, items []string, empty string) {
	procSendMessage.Call(list, lbResetContent, 0, 0)
	if len(items) == 0 {
		addListTo(list, empty)
		return
	}
	for _, item := range items {
		addListTo(list, item)
	}
}

func choose(list uintptr, idx int) {
	if idx >= 0 {
		procSendMessage.Call(list, lbSetCurSel, uintptr(idx), 0)
	}
}

func cur(list uintptr) int {
	idx, _, _ := procSendMessage.Call(list, lbGetCurSel, 0, 0)
	if int32(idx) < 0 {
		return -1
	}
	return int(idx)
}

func selectedNetName() string {
	i := cur(admNetList)
	if i < 0 || i >= len(admNetNames) {
		return ""
	}
	return admNetNames[i]
}

func selectedNetwork() (proto.AdminNet, bool) {
	name := selectedNetName()
	if name == "" {
		return proto.AdminNet{}, false
	}
	admMu.Lock()
	defer admMu.Unlock()
	for _, net := range admNets {
		if net.Name == name {
			return net, true
		}
	}
	return proto.AdminNet{Name: name}, true
}

func selectedIP() string {
	i := cur(admPeerList)
	if i < 0 || i >= len(admPeerIPs) {
		return ""
	}
	return admPeerIPs[i]
}

func selectedBlockedHost() string {
	i := cur(admBlockList)
	if i < 0 || i >= len(admBlockHosts) {
		return ""
	}
	return admBlockHosts[i]
}

func selectedCfgID() string {
	i := cur(admCfgList)
	if i < 0 || i >= len(admCfgIDs) {
		return ""
	}
	return admCfgIDs[i]
}

func selectedCfgLabel() string {
	i := cur(admCfgList)
	if i < 0 || i >= len(admCfgLabels) {
		return ""
	}
	return admCfgLabels[i]
}

func needNetwork() (string, bool) {
	name := selectedNetName()
	if name == "" {
		paintStatus("Сначала выберите сеть слева")
		return "", false
	}
	return name, true
}

func netsSignature(nets []proto.AdminNet) string {
	var b strings.Builder
	for _, n := range nets {
		fmt.Fprintf(&b, "%s\t%d\n", n.Name, n.Online)
		for _, p := range n.Peers {
			fmt.Fprintf(&b, "p\t%s\t%s\t%s\t%s\t%d\t%s\n", p.IP, p.Hostname, p.Endpoint, p.Path, p.Joined, p.Config)
		}
		for _, p := range n.Blocked {
			fmt.Fprintf(&b, "b\t%s\t%s\n", p.IP, p.Hostname)
		}
		for _, c := range n.Configs {
			fmt.Fprintf(&b, "c\t%s\t%s\n", c.ID, c.Label)
		}
		for _, p := range n.Panels {
			fmt.Fprintf(&b, "a\t%s\t%s\t%s\t%v\t%v\t%d\t%s\n", p.ID, p.Label, "", p.Disabled, p.Online, p.LastSeen, p.LastHost)
		}
	}
	return b.String()
}

func addListTo(list uintptr, text string) {
	ptr, _ := syscall.UTF16PtrFromString(text)
	procSendMessage.Call(list, lbAddString, 0, uintptr(unsafe.Pointer(ptr)))
}

func createNetwork() {
	name, password, ok := askForm("Новая сеть", "Имя сети", "Пароль", "", true)
	if !ok {
		return
	}
	if len(password) < 4 {
		paintStatus("Пароль должен быть не короче 4 символов")
		return
	}
	runAdm(func() error { return admSess.CreateNetwork(strings.TrimSpace(name), password) })
}

func deleteNetwork() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	if !confirmBox(fmt.Sprintf("Удалить сеть %s?", name)) {
		return
	}
	runAdm(func() error { return admSess.DeleteNetwork(name) })
}

func changePassword() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	password, _, ok := askForm("Пароль сети "+name, "Новый пароль", "", "", true)
	if !ok {
		return
	}
	if len(password) < 4 {
		paintStatus("Пароль должен быть не короче 4 символов")
		return
	}
	runAdm(func() error { return admSess.SetPassword(name, password) })
}

func createConfig() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	label, _, ok := askForm("Файл для сети "+name, "Имя файла", "", uniqueConfigLabel(), false)
	if !ok {
		return
	}
	label = strings.TrimSpace(label)
	if label == "" {
		paintStatus("Введите имя файла")
		return
	}
	submitConfig(name, label, "")
}

var errNeedPass = errors.New("нужен пароль сети")

var cfgRetry struct{ net, label string }

func submitConfig(name, label, password string) {
	runAdm(func() error {
		if err := admSess.CreateConfig(name, label, password); err != nil {
			if password == "" && !admLimited && strings.Contains(err.Error(), "укажите пароль сети") {
				admMu.Lock()
				cfgRetry.net, cfgRetry.label = name, label
				admMu.Unlock()
				return errNeedPass
			}
			return err
		}
		admMu.Lock()
		admNote = fmt.Sprintf("Файл \"%s\" создан", label)
		admNoteAt = time.Now()
		admMu.Unlock()
		return nil
	})
}

func askConfigPassword() {
	admMu.Lock()
	name, label := cfgRetry.net, cfgRetry.label
	admMu.Unlock()
	if name == "" {
		return
	}
	pass, _, ok := askForm("Пароль сети "+name, "Сеть не помнит свой пароль. Введите его один раз", "", "", true)
	if !ok {
		return
	}
	if len(pass) < 4 {
		paintStatus("Пароль должен быть не короче 4 символов")
		return
	}
	submitConfig(name, label, pass)
}

func uniqueConfigLabel() string {
	net, ok := selectedNetwork()
	used := map[string]bool{}
	if ok {
		for _, cfg := range net.Configs {
			used[strings.ToLower(cfg.Label)] = true
		}
	}

	prefix := "пк"
	if currentLang() == langEN {
		prefix = "pc"
	}
	for i := 2; i < 100; i++ {
		cand := fmt.Sprintf("%s-%d", prefix, i)
		if !used[strings.ToLower(cand)] {
			return cand
		}
	}
	return fmt.Sprintf("%s-%d", prefix, time.Now().Unix()%1000)
}

func saveConfig() {
	saveConnection(false)
}

func saveLinux() {
	saveConnection(true)
}

func saveConnection(linux bool) {
	name, ok := needNetwork()
	if !ok {
		return
	}
	id := selectedCfgID()
	if id == "" {
		paintStatus("Сначала выберите файл справа")
		return
	}
	admMu.Lock()
	if admBusy || admSess == nil {
		admMu.Unlock()
		return
	}
	admBusy = true
	listGen++
	sess := admSess
	admMu.Unlock()
	paintStatus("Сохранение файла...")
	go func() {
		label, server, network, password, err := sess.Config(name, id)
		var path string
		if err == nil {
			if linux {
				path, err = writeConnectionLinux(label, server, network, password, id)
			} else {
				path, err = writeConnectionExe(label, server, network, password, id)
			}
		}
		admMu.Lock()
		admBusy = false
		admErr = nil
		admNoteAt = time.Now()
		if err != nil {
			admNote = err.Error()
		} else {
			admNote = fmt.Sprintf("Файл сохранён: %s", path)
		}
		hwnd := admHwnd
		admMu.Unlock()
		pokeApply(hwnd)
	}()
}

func deleteConfig() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	id := selectedCfgID()
	label := selectedCfgLabel()
	if id == "" {
		paintStatus("Сначала выберите файл справа")
		return
	}
	if !confirmBox(fmt.Sprintf("Удалить файл %s?", label)) {
		return
	}
	runAdm(func() error { return admSess.DeleteConfig(name, id) })
}

func disconnectPeer() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	ip := selectedIP()
	if ip == "" {
		paintStatus("Сначала выберите участника в среднем списке")
		return
	}
	if !confirmBox(fmt.Sprintf("Отключить %s от сети %s?\nПовторный вход будет закрыт, пока не нажмёте \"Разрешить\".", ip, name)) {
		return
	}
	runAdm(func() error { return admSess.Disconnect(name, ip) })
}

func allowPeer() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	host := selectedBlockedHost()
	if host == "" {
		paintStatus("Сначала выберите участника в списке \"Отключены\"")
		return
	}
	runAdm(func() error { return admSess.Allow(name, host) })
}

func disconnectAllPeers() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	if !confirmBox(fmt.Sprintf("Отключить всех в сети %s?\nПовторный вход будет закрыт, пока не нажмёте \"Разрешить\".", name)) {
		return
	}
	runAdm(func() error { return admSess.DisconnectAll(name) })
}

func createPanel() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	owner, _, ok := askForm("Панель для сети "+name, "Кому выдаём (имя)", "", "", false)
	if !ok {
		return
	}
	if owner == "" {
		paintStatus("Введите имя владельца панели")
		return
	}
	fileName := panelFileName()
	runAdm(func() error {
		pf, err := admSess.CreatePanel(name, owner)
		if err != nil {
			return err
		}
		return savePanelExe(pf, fileName)
	})
}

func resetPanel() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	id := selectedPanelID()
	if id == "" {
		paintStatus("Сначала выберите панель в списке")
		return
	}
	if !confirmBox(fmt.Sprintf("Выдать панели \"%s\" новый ключ?\nСтарый exe перестанет работать, а новый нужно отправить владельцу.", selectedPanelName())) {
		return
	}
	fileName := panelFileName()
	runAdm(func() error {
		pf, err := admSess.ResetPanel(name, id)
		if err != nil {
			return err
		}
		return savePanelExe(pf, fileName)
	})
}

func togglePanel() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	i := curPanel()
	if i < 0 || i >= len(admPanelIDs) {
		paintStatus("Сначала выберите панель в списке")
		return
	}
	id, off := admPanelIDs[i], admPanelOff[i]
	runAdm(func() error { return admSess.SetPanelDisabled(name, id, !off) })
}

func deletePanel() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	id := selectedPanelID()
	if id == "" {
		paintStatus("Сначала выберите панель в списке")
		return
	}
	if !confirmBox(fmt.Sprintf("Удалить панель \"%s\"?\nЕё exe перестанет работать.", selectedPanelName())) {
		return
	}
	runAdm(func() error { return admSess.DeletePanel(name, id) })
}

func selectedPanelName() string {
	i := curPanel()
	if i < 0 || i >= len(admPanelNames) {
		return ""
	}
	return admPanelNames[i]
}

func panelFileName() string {
	return tr("Панель %s")
}

func savePanelExe(pf vpn.PanelFile, fileName string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	base, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	path, err := writeStampedFile(base, stamp.File{
		Server:    pf.Server,
		ServerKey: serverKeyText(),
		Network:   pf.Network,
		Password:  pf.Secret,
		Label:     pf.Label,
		Role:      stamp.RolePanel,
		AdminID:   pf.ID,
	}, fmt.Sprintf(fileName, pf.Label), ".exe", downloadPanelsDir)
	if err != nil {
		return err
	}
	admMu.Lock()
	admNote = fmt.Sprintf("Панель сохранена: %s", path)
	admNoteAt = time.Now()
	admMu.Unlock()
	return nil
}

func copyNetworkPassword() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	if !confirmBox(fmt.Sprintf("Скопировать пароль сети %s в буфер обмена?", name)) {
		return
	}
	runAdm(func() error {
		pass, err := admSess.NetworkPassword(name)
		if err != nil {
			return err
		}
		if err := copyText(pass); err != nil {
			return err
		}
		admMu.Lock()
		admNote = "Пароль сети скопирован"
		admNoteAt = time.Now()
		admMu.Unlock()
		return nil
	})
}

func renameConfig() {
	name, ok := needNetwork()
	if !ok {
		return
	}
	id := selectedCfgID()
	label := selectedCfgLabel()
	if id == "" {
		paintStatus("Сначала выберите файл справа")
		return
	}
	next, _, ok := askForm("Переименовать файл", "Новое имя", "", label, false)
	if !ok {
		return
	}
	runAdm(func() error { return admSess.RenameConfig(name, id, strings.TrimSpace(next)) })
}

type adminPrefs struct {
	Server string            `json:"server"`
	Keys   map[string]string `json:"keys,omitempty"`
}

func localAppData() string {
	if base := os.Getenv("LOCALAPPDATA"); base != "" {
		return base
	}
	return os.TempDir()
}

func adminPrefsPath() string {
	return filepath.Join(localAppData(), "Lokalka", "admin.json")
}

func loadAdminPrefs() adminPrefs {
	var prefs adminPrefs
	b, err := os.ReadFile(adminPrefsPath())
	if err == nil {
		_ = json.Unmarshal(b, &prefs)
	}
	if prefs.Keys == nil {
		prefs.Keys = map[string]string{}
	}
	return prefs
}

func adminServerPreset() string {
	server := strings.TrimSpace(loadAdminPrefs().Server)
	if server == "" {
		return defaultServer
	}
	return server
}

func saveAdminServer(server string, key []byte) error {
	server = strings.TrimSpace(server)
	if server == "" {
		return nil
	}
	prefs := loadAdminPrefs()
	prefs.Server = server
	if len(key) != 0 {
		prefs.Keys[server] = vpn.FormatServerKey(key)
	}
	path := adminPrefsPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(prefs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, body, 0o600)
}

func serverKeyText() string {
	admMu.Lock()
	sess := admSess
	admMu.Unlock()
	if sess == nil {
		return ""
	}
	if key := sess.ServerKey(); len(key) != 0 {
		return vpn.FormatServerKey(key)
	}
	return ""
}

func runAdm(fn func() error) {
	admMu.Lock()
	if admBusy || admSess == nil {
		admMu.Unlock()
		paintStatus("Подождите, выполняется другое действие")
		return
	}
	admBusy = true
	listGen++
	admNote = ""
	admMu.Unlock()
	paintStatus("Выполняется...")
	go func() {
		err := fn()
		admMu.Lock()
		admBusy = false
		needPass := errors.Is(err, errNeedPass)
		if err != nil && !needPass {
			admNote = err.Error()
			admNoteAt = time.Now()
		}
		admErr = nil
		hwnd := admHwnd
		admMu.Unlock()
		requestRefresh()
		pokeApply(hwnd)
		if needPass && hwnd != 0 {
			atomic.StoreUint32(&uiPass, 1)
			procPostMessage.Call(hwnd, wmPoke, 0, 0)
		}
	}()
}

func askForm(title, labelA, labelB, presetA string, secretB bool) (string, string, bool) {
	resetFormFlags()
	owner := admHwnd
	dlg = dialogState{needB: labelB != "", owner: owner}
	height := uintptr(260)
	if dlg.needB {
		height = 340
	}
	hwnd := newWindow("LokalkaForm", formProc, title, 520, height, false, owner)
	if hwnd == 0 {
		return "", "", false
	}
	dlg.hwnd = hwnd
	if owner != 0 {
		procEnableWindow.Call(owner, 0)
	}
	control(hwnd, "STATIC", labelA, 28, 22, 448, 24, wsChild|wsVisible, 0, admFont)
	dlg.editA = makeField(hwnd, presetA, 28, 50, 448, 40, secretB && !dlg.needB, 0, "")
	next := 104
	if dlg.needB {
		control(hwnd, "STATIC", labelB, 28, 102, 448, 24, wsChild|wsVisible, 0, admFont)
		dlg.editB = makeField(hwnd, "", 28, 130, 448, 40, secretB, 0, "")
		next = 184
	}
	dlg.hint = control(hwnd, "STATIC", "", 28, next, 448, 26, wsChild|wsVisible, 0, admFont)
	admButton(hwnd, "Готово", 28, next+32, 150, 42, idOK)
	admButton(hwnd, "Отмена", 190, next+32, 150, 42, idExit)
	show(hwnd)
	procSetFocus.Call(dlg.editA)
	procSendMessage.Call(dlg.editA, 0x00B1, 0, ^uintptr(0))
	dialogLoop()
	releaseOwner()
	return strings.TrimSpace(dlg.a), dlg.b, dlg.ok
}

func releaseOwner() {
	if dlg.owner != 0 {
		procEnableWindow.Call(dlg.owner, 1)
		procSetForeground.Call(dlg.owner)
		dlg.owner = 0
	}
}

func closeForm() {
	if dlg.hwnd == 0 || dlg.done {
		return
	}
	releaseOwner()
	procDestroyWindow.Call(dlg.hwnd)
}

func formProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	if r, ok := themeProc(hwnd, msg, wparam, lparam); ok {
		return r
	}
	switch msg {
	case wmAdm:
		atomic.StoreUint32(&loginReady, 1)
		return 0
	case wmCommand:
		if wparam&0xffff == idOK && wparam>>16 == 0 {
			atomic.StoreUint32(&formClick, 1)
			return 0
		}
		if wparam&0xffff == idExit && wparam>>16 == 0 {
			atomic.StoreUint32(&formClose, 1)
			return 0
		}
	case wmClose:
		atomic.StoreUint32(&formClose, 1)
		return 0
	case wmDestroy:
		admMu.Lock()
		dialGen++
		admMu.Unlock()
		dlg.done = true
		dlg.hwnd = 0
		return 0
	}
	r, _, _ := procDefWindow.Call(hwnd, msg, wparam, lparam)
	return r
}

func startLogin(hwnd uintptr) {
	if dlg.dialing {
		return
	}
	server, err := vpn.ServerAddr(dlg.a)
	if err != nil {
		setText(dlg.hint, err.Error())
		return
	}
	if server != strings.TrimSpace(dlg.a) {
		dlg.a = server
		setText(dlg.editA, server)
	}
	password := dlg.b
	if password == "" {
		setText(dlg.hint, "Введите пароль")
		return
	}
	dlg.dialing = true
	setText(dlg.hint, "Подключение...")
	admMu.Lock()
	dialGen++
	gen := dialGen
	admMu.Unlock()
	confirmed := dlg.pin
	if dlg.pinServer != server {
		confirmed = nil
	}
	go func() {
		pin := confirmed
		if pin == nil {
			pin, _ = vpn.ParseServerKey(loadAdminPrefs().Keys[server])
		}
		if pin == nil {
			key, err := vpn.ProbeServerKey(server)
			admMu.Lock()
			if gen == dialGen {
				loginProbe, loginErr = key, err
			}
			admMu.Unlock()
			procPostMessage.Call(hwnd, wmAdm, 0, 0)
			return
		}
		sess, err := vpn.DialAdmin(server, password, pin)
		admMu.Lock()
		stale := gen != dialGen
		if stale {
			admMu.Unlock()
			if sess != nil {
				sess.Close()
			}
			return
		}
		loginSess = sess
		loginErr = err
		admMu.Unlock()
		procPostMessage.Call(hwnd, wmAdm, 0, 0)
	}()
}

func finishLogin() {
	admMu.Lock()
	sess := loginSess
	err := loginErr
	probe := loginProbe
	loginSess = nil
	loginErr = nil
	loginProbe = nil
	admMu.Unlock()
	if dlg.done || !dlg.login {
		if sess != nil {
			sess.Close()
		}
		return
	}
	dlg.dialing = false
	if err != nil {
		if sess != nil {
			sess.Close()
		}
		if errors.Is(err, vpn.ErrServerKey) {
			setText(dlg.hint, "Ключ сервера не совпал с запомненным. Если сервер переустанавливали, удалите "+adminPrefsPath())
			return
		}
		setText(dlg.hint, err.Error())
		return
	}
	if probe != nil {
		confirmServerKey(probe)
		return
	}
	if sess == nil {
		setText(dlg.hint, "сервер не ответил")
		return
	}
	admMu.Lock()
	loginSess = sess
	admMu.Unlock()
	_ = saveAdminServer(strings.TrimSpace(dlg.a), sess.ServerKey())
	dlg.ok = true
	closeForm()
}

func confirmServerKey(key []byte) {
	server := strings.TrimSpace(dlg.a)
	text := fmt.Sprintf("Первый вход на сервер %s.\n\nКлюч сервера:\n%s\n\nСверьте его с ключом, который показала установка сервера (на сервере его выводит команда \"lokalka key\"). Ключи совпадают?",
		server, groupKey(vpn.FormatServerKey(key)))
	if !confirmOwned(dlg.hwnd, text) {
		setText(dlg.hint, "Вход отменён: ключ сервера не подтверждён")
		return
	}
	dlg.pin, dlg.pinServer = key, server
	startLogin(dlg.hwnd)
	if err := saveAdminServer(server, key); err != nil {
		setText(dlg.hint, fmt.Sprintf("Подключение... Ключ не удалось запомнить (%v), при следующем входе он будет показан снова", err))
	}
}

func groupKey(s string) string {
	var b strings.Builder
	for i, r := range s {
		if i > 0 && i%4 == 0 {
			b.WriteByte(' ')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func dialogLoop() {
	var m winMsg
	for !dlg.done {
		r, _, _ := procGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslate.Call(uintptr(unsafe.Pointer(&m)))
		procDispatch.Call(uintptr(unsafe.Pointer(&m)))
		flushUI()
	}
}

func resetFormFlags() {
	atomic.StoreUint32(&formClick, 0)
	atomic.StoreUint32(&formClose, 0)
	atomic.StoreUint32(&loginReady, 0)
}

func acceptFormClick() {
	if dlg.done || dlg.hwnd == 0 {
		return
	}
	if dlg.dialing && dlg.login {
		return
	}
	dlg.a = windowText(dlg.editA)
	dlg.b = windowText(dlg.editB)
	if strings.TrimSpace(dlg.a) == "" {
		setText(dlg.hint, "Заполните первое поле")
		return
	}
	if dlg.login {
		startLogin(dlg.hwnd)
		return
	}
	dlg.ok = true
	closeForm()
}

func flushAdminUI() {
	flushDepth++
	defer func() { flushDepth-- }()
	closed := atomic.SwapUint32(&formClose, 0) == 1
	if closed {
		closeForm()
	}
	if !closed && atomic.SwapUint32(&formClick, 0) == 1 {
		acceptFormClick()
	}
	if atomic.SwapUint32(&loginReady, 0) == 1 {
		finishLogin()
	}
	if flushDepth > 1 {
		return
	}
	for i := 0; i < 4; i++ {
		if atomic.SwapUint32(&admClose, 0) == 1 && admHwnd != 0 {
			procDestroyWindow.Call(admHwnd)
			return
		}
		if atomic.SwapUint32(&clearFilter, 0) == 1 && admFilter != 0 {
			setText(admFilter, "")
		}
		cmd := atomic.SwapUintptr(&uiCmd, 0)
		pending := cmd != 0 || atomic.LoadUint32(&uiFetch) == 1 || atomic.LoadUint32(&uiApply) == 1 || atomic.LoadUint32(&uiSel) == 1 || atomic.LoadUint32(&uiFilter) == 1 || atomic.LoadUint32(&uiLayout) == 1 || atomic.LoadUint32(&uiPass) == 1
		if !pending {
			return
		}
		runAdminCmd(cmd)
		if atomic.SwapUint32(&uiPass, 0) == 1 {
			askConfigPassword()
		}
		if atomic.SwapUint32(&uiFetch, 0) == 1 {
			requestRefresh()
		}
		if atomic.SwapUint32(&uiApply, 0) == 1 {
			applyAdmin()
		}
		if atomic.SwapUint32(&uiSel, 0) == 1 {
			showNetwork()
		}
		if atomic.SwapUint32(&uiFilter, 0) == 1 {
			showNetwork()
		}
		if atomic.SwapUint32(&uiLayout, 0) == 1 && admHwnd != 0 {
			layoutAdmin(admHwnd)
		}
	}
}

func runAdminCmd(cmd uintptr) {
	switch cmd {
	case idAdmUpdate:
		admMu.Lock()
		admNote = ""
		admDenied = false
		admMu.Unlock()
		atomic.StoreUint32(&uiFetch, 1)
	case idAdmTabFiles, idAdmTabPanels:
		showTab(cmd == idAdmTabPanels)
		if admHwnd != 0 {
			layoutAdmin(admHwnd)
		}
	case idAdmNewPanel:
		createPanel()
	case idAdmResetPanel:
		resetPanel()
	case idAdmTogglePanel:
		togglePanel()
	case idAdmDelPanel:
		deletePanel()
	case idAdmNewNet:
		createNetwork()
	case idAdmDelNet:
		deleteNetwork()
	case idAdmPass:
		changePassword()
	case idAdmNewCfg:
		createConfig()
	case idAdmSave:
		saveConfig()
	case idAdmLinux:
		saveLinux()
	case idAdmDelCfg:
		deleteConfig()
	case idAdmKick:
		disconnectPeer()
	case idAdmAllow:
		allowPeer()
	case idAdmKickAll:
		disconnectAllPeers()
	case idAdmCopyPass:
		copyNetworkPassword()
	case idAdmRename:
		renameConfig()
	case idAdmCopyIP:
		copyPeerIP()
	}
}

func copyPeerIP() {
	ip := selectedIP()
	if ip == "" {
		return
	}
	if err := copyText(ip); err != nil {
		setNote(err.Error())
	} else {
		setNote("IP " + ip + " скопирован")
	}
	applyAdmin()
}

func setNote(text string) {
	admMu.Lock()
	admNote = text
	admNoteAt = time.Now()
	admMu.Unlock()
}

func paintStatus(text string) {
	switch {
	case strings.HasPrefix(text, "На связи"), strings.HasPrefix(text, "Файл сохранён"), strings.HasPrefix(text, "Панель сохранена"),
		strings.HasSuffix(text, "скопирован"), strings.HasSuffix(text, "создан"):
		statusTone = rgb(0x1f, 0x6b, 0x4a)
	case text == "Загрузка..." || text == "Выполняется..." || text == "Сохранение файла..." || strings.HasPrefix(text, "Сначала"):
		statusTone = rgb(0x5c, 0x56, 0x4c)
	default:
		statusTone = rgb(0x8d, 0x3b, 0x32)
	}
	setText(admStatus, text)
}

func ensureUI() {
	if admFont != 0 {
		return
	}
	admFont = makeFont(-16, 400)
	admFontSemi = makeFont(-16, 600)
	admFontTitle = makeFont(-26, 600)
	if admFont == 0 {
		admFont = stockFont()
	}
	if admFontSemi == 0 {
		admFontSemi = admFont
	}
	if admFontTitle == 0 {
		admFontTitle = admFont
	}
	bgBrush, _, _ = procCreateBrush.Call(rgb(0xf4, 0xf1, 0xea))
	listBrush, _, _ = procCreateBrush.Call(rgb(0xff, 0xff, 0xff))
	cardBrush = listBrush
}

func makeFont(height int32, weight uintptr) uintptr {
	name, _ := syscall.UTF16PtrFromString("Segoe UI")
	font, _, _ := procCreateFont.Call(
		uintptr(height),
		0, 0, 0,
		weight,
		0, 0, 0,
		1,
		0, 0,
		5,
		0,
		uintptr(unsafe.Pointer(name)),
	)
	return font
}

func admButton(parent uintptr, text string, x, y, w, h int, id uintptr) uintptr {
	btnLabel[id] = text
	return control(parent, "BUTTON", text, x, y, w, h, wsChild|wsVisible|bsOwnerDraw, id, admFont)
}

func themeProc(hwnd, msg, wparam, lparam uintptr) (uintptr, bool) {
	switch msg {
	case wmCommand:
		code := wparam >> 16
		if wparam&0xffff == idLang && code == 0 {
			atomic.StoreUint32(&uiLangFlip, 1)
			procPostMessage.Call(hwnd, wmPoke, 0, 0)
			return 0, true
		}
		if _, ok := fields[lparam]; ok && (code == enSetFocus || code == enKillFocus || code == enChange) {
			atomic.StoreUint32(&uiFields, 1)
			procPostMessage.Call(hwnd, wmPoke, 0, 0)
		}
		return 0, false
	case wmLButtonDown:
		if edit := fieldAt(hwnd, lparam); edit != 0 {
			atomic.StoreUintptr(&focusWant, edit)
			procPostMessage.Call(hwnd, wmPoke, 0, 0)
			return 0, true
		}
		return 0, false
	case wmPoke:
		return 0, true
	case wmMeasure:
		measureRow(lparam)
		return 1, true
	case wmDrawItem:
		if lparam == 0 {
			return 1, true
		}
		d := (*drawItem)(unsafe.Pointer(lparam))
		if d.ctlType == 2 {
			drawListRow(d)
		} else {
			drawAdmButton(d)
		}
		return 1, true
	case wmErase:
		ensureUI()
		var rc winRect
		procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rc)))
		procFillRect.Call(wparam, uintptr(unsafe.Pointer(&rc)), bgBrush)
		if hwnd == admHwnd && paintCards {
			for _, card := range admCards {
				fillBox(wparam, card, rgb(0xff, 0xff, 0xff), rgb(0xe4, 0xdd, 0xd0))
			}
		}
		if hwnd == mainHwnd && paintMain {
			fillBox(wparam, ipCard, rgb(0xff, 0xff, 0xff), rgb(0xe4, 0xdd, 0xd0))
			fillBox(wparam, mainCard, rgb(0xff, 0xff, 0xff), rgb(0xe4, 0xdd, 0xd0))
			c := dotColor(sourceText(hwndState))
			fillRound(wparam, stateDot, c, c, 10)
		}
		paintBrand(hwnd, wparam)
		paintFields(hwnd, wparam)
		return 1, true
	case wmCtlEdit:
		ensureUI()
		procSetTextColor.Call(wparam, rgb(0x1c, 0x19, 0x15))
		procSetBkColor.Call(wparam, rgb(0xff, 0xff, 0xff))
		procSetBkMode.Call(wparam, 2)
		return listBrush, true
	case wmCtlStatic, wmCtlList:
		ensureUI()
		if msg == wmCtlList {
			procSetTextColor.Call(wparam, rgb(0x1c, 0x19, 0x15))
			procSetBkColor.Call(wparam, rgb(0xff, 0xff, 0xff))
			procSetBkMode.Call(wparam, 2)
			return listBrush, true
		}
		color, brush := staticLook(lparam)
		procSetTextColor.Call(wparam, color)
		if brush == cardBrush || brush == listBrush {
			procSetBkColor.Call(wparam, rgb(0xff, 0xff, 0xff))
		} else {
			procSetBkColor.Call(wparam, rgb(0xf4, 0xf1, 0xea))
		}
		procSetBkMode.Call(wparam, 2)
		return brush, true
	default:
		return 0, false
	}
}

func staticLook(hwnd uintptr) (uintptr, uintptr) {
	ink := rgb(0x1c, 0x19, 0x15)
	muted := rgb(0x5c, 0x56, 0x4c)
	switch hwnd {
	case admTitleNet, admTitlePeer, admTitleBlock, admTitleCfg, hwndNet:
		return muted, cardBrush
	case hwndIP:
		return ink, cardBrush
	case admFooter, pwdHint, loginLead, hwndPeers:
		return muted, bgBrush
	case admStatus:
		tone := statusTone
		if tone == 0 {
			tone = muted
		}
		return tone, bgBrush
	case hwndState:
		return stateColor(sourceText(hwnd)), bgBrush
	default:
		return ink, bgBrush
	}
}

func stateColor(text string) uintptr {
	switch {
	case strings.HasPrefix(text, "Подключено"):
		return rgb(0x1f, 0x6b, 0x4a)
	case strings.HasPrefix(text, "Ошибка"), strings.Contains(text, "Нет связи"):
		return rgb(0x8d, 0x3b, 0x32)
	default:
		return rgb(0x1c, 0x19, 0x15)
	}
}

type measureItem struct {
	ctlType    uint32
	ctlID      uint32
	itemID     uint32
	itemWidth  uint32
	itemHeight uint32
	pad        uint32
	itemData   uintptr
}

func measureRow(lp uintptr) {
	if lp == 0 {
		return
	}
	m := (*measureItem)(unsafe.Pointer(lp))
	if m.ctlType != 2 {
		return
	}
	switch m.ctlID {
	case idPeerList, idClientList, idCfgList, idPanelList:
		m.itemHeight = 52
	default:
		m.itemHeight = 40
	}
}

func drawAdmButton(d *drawItem) {
	ensureUI()
	kind := buttonKind(uintptr(d.ctlID))
	pressed := d.itemState&odsSelected != 0
	face, edge, text := buttonPalette(kind, pressed)
	outer := winRect{d.left, d.top, d.right, d.bottom}
	solidFill(d.hdc, outer, backdrop(d.hwndItem))
	inner := outer
	if inner.right > inner.left {
		inner.right--
	}
	if inner.bottom > inner.top {
		inner.bottom--
	}
	fillRound(d.hdc, inner, face, edge, 16)
	procSetTextColor.Call(d.hdc, text)
	procSetBkMode.Call(d.hdc, 1)
	old, _, _ := procSelectObject.Call(d.hdc, admFont)
	label := tr(btnLabel[uintptr(d.ctlID)])
	if label == "" {
		label = " "
	}
	ptr, _ := syscall.UTF16PtrFromString(label)
	rc := winRect{d.left, d.top, d.right, d.bottom}
	procDrawText.Call(d.hdc, uintptr(unsafe.Pointer(ptr)), ^uintptr(0), uintptr(unsafe.Pointer(&rc)), 0x0825)
	if old != 0 {
		procSelectObject.Call(d.hdc, old)
	}
}

func backdrop(ctl uintptr) uintptr {
	beige := rgb(0xf4, 0xf1, 0xea)
	parent, _, _ := procGetParent.Call(ctl)
	if parent == 0 {
		return beige
	}
	var rc winRect
	procGetWindowRect.Call(ctl, uintptr(unsafe.Pointer(&rc)))
	procMapPoints.Call(0, parent, uintptr(unsafe.Pointer(&rc)), 2)
	var cards []winRect
	if parent == admHwnd && paintCards {
		cards = admCards[:]
	}
	if parent == mainHwnd && paintMain {
		cards = []winRect{mainCard, ipCard}
	}
	for _, c := range cards {
		if rc.left >= c.left && rc.right <= c.right && rc.top >= c.top && rc.bottom <= c.bottom {
			return rgb(0xff, 0xff, 0xff)
		}
	}
	return beige
}

func buttonKind(id uintptr) int {
	switch id {
	case idAdmDelNet, idAdmDelCfg, idAdmKick, idAdmKickAll, idAdmDelPanel:
		return 2
	case idAdmTabFiles:
		if !admTabPanels {
			return 3
		}
		return 1
	case idAdmTabPanels:
		if admTabPanels {
			return 3
		}
		return 1
	case idAdmUpdate, idAdmPass, idAdmCopyPass, idAdmNewCfg, idAdmLinux, idAdmAllow, idAdmRename, idExit, idCopyMyIP,
		idAdmResetPanel, idAdmTogglePanel, idLang:
		return 1
	default:
		return 0
	}
}

func buttonPalette(kind int, pressed bool) (face, edge, text uintptr) {
	switch kind {
	case 2:
		face = rgb(0xff, 0xff, 0xff)
		edge = rgb(0xe0, 0xb2, 0xad)
		text = rgb(0x8d, 0x3b, 0x32)
		if pressed {
			face = rgb(0xf8, 0xec, 0xea)
		}
	case 3:

		face = rgb(0xe7, 0xf3, 0xec)
		edge = rgb(0x1f, 0x6b, 0x4a)
		text = rgb(0x1f, 0x6b, 0x4a)
	case 1:
		face = rgb(0xff, 0xff, 0xff)
		edge = rgb(0xd5, 0xce, 0xc2)
		text = rgb(0x1c, 0x19, 0x15)
		if pressed {
			face = rgb(0xef, 0xea, 0xe2)
		}
	default:
		face = rgb(0x1f, 0x6b, 0x4a)
		text = rgb(0xff, 0xff, 0xff)
		if pressed {
			face = darken(face)
		}
		edge = face
	}
	return face, edge, text
}

func drawListRow(d *drawItem) {
	if d.hwndItem == 0 || d.itemID > 100000 {
		return
	}
	ensureUI()
	text := listItemText(d.hwndItem, int(d.itemID))
	selected := d.itemState&odsSelected != 0
	bg := rgb(0xff, 0xff, 0xff)
	if selected {
		bg = rgb(0xe7, 0xf3, 0xec)
	}
	outer := winRect{d.left, d.top, d.right, d.bottom}
	solidFill(d.hdc, outer, bg)
	if selected {
		bar := winRect{d.left, d.top + 8, d.left + 3, d.bottom - 8}
		solidFill(d.hdc, bar, rgb(0x1f, 0x6b, 0x4a))
	}
	procSetBkMode.Call(d.hdc, 1)

	parts := strings.Split(text, "      ")
	tall := d.bottom-d.top >= 48
	if isHintLine(text) {
		procSetTextColor.Call(d.hdc, rgb(0x8a, 0x84, 0x78))
		old, _, _ := procSelectObject.Call(d.hdc, admFont)
		drawUTF(d.hdc, tr(text), winRect{d.left + 16, d.top, d.right - 12, d.bottom}, 0x8824)
		if old != 0 {
			procSelectObject.Call(d.hdc, old)
		}
		return
	}
	if tall && len(parts) >= 2 {
		drawTallPeerRow(d, parts)
		return
	}
	name := trName(parts[0])
	procSetTextColor.Call(d.hdc, rgb(0x1c, 0x19, 0x15))
	old, _, _ := procSelectObject.Call(d.hdc, admFontSemi)
	textRC := winRect{d.left + 16, d.top, d.right - 12, d.bottom}
	nameRC := textRC
	if len(parts) > 1 {
		width := textWidth(d.hdc, name)
		limit := (d.right - d.left) / 2
		if width > limit {
			width = limit
		}
		nameRC.right = nameRC.left + width
	}
	drawUTF(d.hdc, name, nameRC, 0x8824)
	x := nameRC.right + 14
	for _, part := range parts[1:] {
		if x >= d.right-24 {
			break
		}
		procSetTextColor.Call(d.hdc, partColor(part))
		procSelectObject.Call(d.hdc, admFont)
		shown := tr(part)
		w := textWidth(d.hdc, shown)
		partRC := winRect{x, d.top, x + w, d.bottom}
		if partRC.right > d.right-12 {
			partRC.right = d.right - 12
		}
		drawUTF(d.hdc, shown, partRC, 0x8824)
		x += w + 18
	}
	if old != 0 {
		procSelectObject.Call(d.hdc, old)
	}
}

func trName(s string) string {
	if s == "без имени" {
		return tr(s)
	}
	return s
}

func partColor(part string) uintptr {
	switch {
	case part == "это вы", part == "напрямую", strings.HasPrefix(part, "UDP есть"), part == "в панели сейчас":
		return rgb(0x1f, 0x6b, 0x4a)
	case strings.HasPrefix(part, "нет UDP"), part == "приостановлена":
		return rgb(0x8d, 0x3b, 0x32)
	default:
		return rgb(0x6d, 0x67, 0x5d)
	}
}

func drawTallPeerRow(d *drawItem, parts []string) {
	ink := rgb(0x1c, 0x19, 0x15)
	muted := rgb(0x6d, 0x67, 0x5d)
	mid := d.top + (d.bottom-d.top)/2
	topRC := winRect{d.left + 16, d.top + 2, d.right - 12, mid + 2}
	botRC := winRect{d.left + 16, mid - 2, d.right - 12, d.bottom - 2}

	old, _, _ := procSelectObject.Call(d.hdc, admFontSemi)
	procSetTextColor.Call(d.hdc, ink)
	ip := trName(parts[0])
	ipW := textWidth(d.hdc, ip)
	ipRC := topRC
	ipRC.right = ipRC.left + ipW
	if ipRC.right > topRC.right {
		ipRC.right = topRC.right
	}
	drawUTF(d.hdc, ip, ipRC, 0x8824)

	if len(parts) > 1 && parts[1] != "" && ipRC.right+12 < topRC.right {
		procSelectObject.Call(d.hdc, admFont)
		procSetTextColor.Call(d.hdc, muted)
		hostRC := topRC
		hostRC.left = ipRC.right + 12
		drawUTF(d.hdc, trName(parts[1]), hostRC, 0x8824)
	}

	x := botRC.left
	procSelectObject.Call(d.hdc, admFont)
	for _, part := range parts[2:] {
		if x >= botRC.right-8 {
			break
		}
		procSetTextColor.Call(d.hdc, partColor(part))
		shown := tr(part)
		w := textWidth(d.hdc, shown)
		partRC := winRect{x, botRC.top, x + w, botRC.bottom}
		if partRC.right > botRC.right {
			partRC.right = botRC.right
		}
		drawUTF(d.hdc, shown, partRC, 0x8824)
		x += w + 14
	}
	if old != 0 {
		procSelectObject.Call(d.hdc, old)
	}
}

func isHintLine(text string) bool {
	switch text {
	case "Сетей ещё нет", "Выберите сеть слева", "Сейчас никого нет", "Файлов ещё нет",
		"Пока никого нет", "Других участников пока нет", "Никого по фильтру", "Никого не отключали", "Панелей ещё нет":
		return true
	default:
		return false
	}
}

func listItemText(list uintptr, idx int) string {
	if list == 0 || idx < 0 {
		return ""
	}
	buf := make([]uint16, 512)
	procSendMessage.Call(list, 0x0189, uintptr(idx), uintptr(unsafe.Pointer(&buf[0])))
	return syscall.UTF16ToString(buf)
}

func textWidth(hdc uintptr, text string) int32 {
	if text == "" {
		return 0
	}
	rc := winRect{0, 0, 4000, 40}
	ptr, _ := syscall.UTF16PtrFromString(text)
	procDrawText.Call(hdc, uintptr(unsafe.Pointer(ptr)), ^uintptr(0), uintptr(unsafe.Pointer(&rc)), 0x0C20)
	if rc.right < 0 {
		return 0
	}
	return rc.right
}

func drawUTF(hdc uintptr, text string, rc winRect, flags uintptr) {
	if text == "" {
		text = " "
	}
	ptr, _ := syscall.UTF16PtrFromString(text)
	procDrawText.Call(hdc, uintptr(unsafe.Pointer(ptr)), ^uintptr(0), uintptr(unsafe.Pointer(&rc)), flags)
}

func solidFill(hdc uintptr, rc winRect, color uintptr) {
	brush, _, _ := procCreateBrush.Call(color)
	if brush == 0 {
		return
	}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rc)), brush)
	procDeleteObject.Call(brush)
}

func fillBox(hdc uintptr, rc winRect, fill, edge uintptr) {
	if rc.right <= rc.left || rc.bottom <= rc.top {
		return
	}
	stroke(hdc, rc, fill, edge, false, 0)
}

func fillRound(hdc uintptr, rc winRect, fill, edge uintptr, radius int32) {
	if rc.right <= rc.left || rc.bottom <= rc.top {
		return
	}
	stroke(hdc, rc, fill, edge, true, radius)
}

func stroke(hdc uintptr, rc winRect, fill, edge uintptr, round bool, radius int32) {
	pen, _, _ := procCreatePen.Call(0, 1, edge)
	brush, _, _ := procCreateBrush.Call(fill)
	if pen == 0 || brush == 0 {
		if pen != 0 {
			procDeleteObject.Call(pen)
		}
		if brush != 0 {
			procDeleteObject.Call(brush)
		}
		return
	}
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	if round {
		procRoundRect.Call(hdc, uintptr(rc.left), uintptr(rc.top), uintptr(rc.right), uintptr(rc.bottom), uintptr(radius), uintptr(radius))
	} else {
		procRectangle.Call(hdc, uintptr(rc.left), uintptr(rc.top), uintptr(rc.right), uintptr(rc.bottom))
	}
	if oldPen != 0 {
		procSelectObject.Call(hdc, oldPen)
	}
	if oldBrush != 0 {
		procSelectObject.Call(hdc, oldBrush)
	}
	procDeleteObject.Call(pen)
	procDeleteObject.Call(brush)
}

func darken(c uintptr) uintptr {
	r := byte(c) * 8 / 10
	g := byte(c>>8) * 8 / 10
	b := byte(c>>16) * 8 / 10
	return rgb(r, g, b)
}

func rgb(r, g, b byte) uintptr {
	return uintptr(r) | uintptr(g)<<8 | uintptr(b)<<16
}

func writeConnectionExe(label, server, network, password, configID string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	base, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	return writeStamped(base, label, server, network, password, configID, ".exe")
}

func writeConnectionLinux(label, server, network, password, configID string) (string, error) {
	base, err := linuxClientBytes()
	if err != nil {
		return "", err
	}
	return writeStamped(base, label, server, network, password, configID, "-linux-amd64")
}

func linuxClientBytes() ([]byte, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(filepath.Dir(exe), "lokalka-linux-amd64")
	base, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("рядом с программой нет lokalka-linux-amd64")
	}
	if len(base) < 4 || base[0] != 0x7f || base[1] != 'E' || base[2] != 'L' || base[3] != 'F' {
		return nil, errors.New("файл lokalka-linux-amd64 повреждён")
	}
	return base, nil
}

const (
	downloadConnectDir = "Files - Connect"
	downloadPanelsDir  = "Files - Panels"
)

func writeStamped(base []byte, label, server, network, password, configID, suffix string) (string, error) {
	return writeStampedFile(base, stamp.File{
		Server:    server,
		ServerKey: serverKeyText(),
		Network:   network,
		Password:  password,
		ConfigID:  configID,
		Label:     label,
	}, label, suffix, downloadConnectDir)
}

func writeStampedFile(base []byte, file stamp.File, name, suffix, sub string) (string, error) {
	out, err := stamp.Append(stamp.Strip(base), file)
	if err != nil {
		return "", err
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(filepath.Dir(exe), "download", sub, connectionStem(file.Network))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := uniqueExe(dir, connectionStem(name)+suffix)
	if err := os.WriteFile(path, out, 0o755); err != nil {
		return "", err
	}
	return path, nil
}

func connectionFileName(label string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(label) {
		switch {
		case r < 32, strings.ContainsRune(`<>:"/\|?*`, r):
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), " .")
	if name == "" || len(name) > 40 {
		if len(name) > 40 {
			name = strings.Trim(name[:40], " .")
		}
		if name == "" {
			name = "lokalka"
		}
	}
	return name + ".exe"
}

func connectionStem(label string) string {
	name := connectionFileName(label)
	return strings.TrimSuffix(name, ".exe")
}

func uniqueExe(dir, name string) string {
	path := filepath.Join(dir, name)
	if _, err := os.Stat(path); err != nil {
		return path
	}
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	for i := 2; i < 1000; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s-%d%s", base, i, ext))
		if _, err := os.Stat(candidate); err != nil {
			return candidate
		}
	}
	return path
}
