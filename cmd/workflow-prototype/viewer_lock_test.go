//go:build darwin || linux

package main

import "testing"

func TestCleanupSkipsWhileAnotherViewerIsOpen(t *testing.T) {
	directory := t.TempDir()
	first, err := openViewerLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := openViewerLock(directory)
	if err != nil {
		t.Fatal(err)
	}
	cleaned := 0
	if err := first.cleanup(func() { cleaned++ }); err != nil {
		t.Fatal(err)
	}
	if err := second.cleanup(func() { cleaned++ }); err != nil {
		t.Fatal(err)
	}
	if cleaned != 0 {
		t.Fatal("cleanup ran while two viewers were open")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.cleanup(func() { cleaned++ }); err != nil {
		t.Fatal(err)
	}
	if cleaned != 1 {
		t.Fatal("cleanup did not resume after the other viewer closed")
	}
}
