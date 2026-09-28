//go:build !darwin && !linux

package main

import "errors"

type viewerLock struct{}

func openViewerLock(string) (*viewerLock, error) {
	return nil, errors.New("workflow viewer locking requires Linux or macOS")
}
func (*viewerLock) Close() error         { return nil }
func (*viewerLock) cleanup(func()) error { return nil }
