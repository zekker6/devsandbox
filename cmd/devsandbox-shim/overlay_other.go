//go:build !linux

package main

func execWithOverlays(uid, gid int, overlays []overlayEntry, args []string) {
	fatal("overlayfs requires Linux — this code path should not be reached on this platform")
}

func overlayChild() {
	fatal("overlay child requires Linux — this code path should not be reached on this platform")
}
