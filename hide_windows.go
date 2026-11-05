//go:build windows

package main

import "syscall"

// hideFolder marks the app's own data folder hidden in Explorer.
func hideFolder(p string) {
	if ptr, err := syscall.UTF16PtrFromString(p); err == nil {
		if attrs, err := syscall.GetFileAttributes(ptr); err == nil {
			_ = syscall.SetFileAttributes(ptr, attrs|syscall.FILE_ATTRIBUTE_HIDDEN)
		}
	}
}
