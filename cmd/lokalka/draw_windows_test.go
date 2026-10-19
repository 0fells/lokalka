//go:build windows

package main

import (
	"testing"
	"unsafe"
)

func TestDrawItemLayout(t *testing.T) {
	var d drawItem
	if unsafe.Sizeof(d) != 64 {
		t.Fatalf("DRAWITEMSTRUCT size = %d, want 64", unsafe.Sizeof(d))
	}
	if unsafe.Offsetof(d.hwndItem) != 24 || unsafe.Offsetof(d.hdc) != 32 || unsafe.Offsetof(d.left) != 40 || unsafe.Offsetof(d.itemData) != 56 {
		t.Fatalf("layout hwnd=%d hdc=%d left=%d data=%d", unsafe.Offsetof(d.hwndItem), unsafe.Offsetof(d.hdc), unsafe.Offsetof(d.left), unsafe.Offsetof(d.itemData))
	}
	var m measureItem
	if unsafe.Sizeof(m) != 32 || unsafe.Offsetof(m.itemHeight) != 16 {
		t.Fatalf("MEASUREITEMSTRUCT size=%d height=%d", unsafe.Sizeof(m), unsafe.Offsetof(m.itemHeight))
	}
}
