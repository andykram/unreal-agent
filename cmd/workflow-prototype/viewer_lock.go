//go:build darwin || linux

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// viewerLock holds a shared state-directory lock while a simulator is open.
// A separate gate serializes attempts to upgrade to an exclusive cleanup lock.
type viewerLock struct {
	file *os.File
	gate *os.File
}

func openViewerLock(stateDir string) (*viewerLock, error) {
	if err := os.MkdirAll(stateDir, 0700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(stateDir, ".viewers.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	gate, err := os.OpenFile(filepath.Join(stateDir, ".cleanup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		file.Close()
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_SH); err != nil {
		gate.Close()
		file.Close()
		return nil, err
	}
	return &viewerLock{file: file, gate: gate}, nil
}

func (lock *viewerLock) Close() error {
	gateErr := lock.gate.Close()
	fileErr := lock.file.Close()
	return errors.Join(gateErr, fileErr)
}

func (lock *viewerLock) cleanup(run func()) error {
	gateFD := int(lock.gate.Fd())
	err := syscall.Flock(gateFD, syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("acquire cleanup gate: %w", err)
	}
	defer syscall.Flock(gateFD, syscall.LOCK_UN)
	viewerFD := int(lock.file.Fd())
	if err := syscall.Flock(viewerFD, syscall.LOCK_UN); err != nil {
		return fmt.Errorf("release viewer lock: %w", err)
	}
	err = syscall.Flock(viewerFD, syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		run()
	}
	// Keep the gate until the shared viewer lock is restored, so two viewers
	// cannot both release their shared locks and clean up one another's runs.
	restoreErr := syscall.Flock(viewerFD, syscall.LOCK_SH)
	if err != nil && !errors.Is(err, syscall.EWOULDBLOCK) {
		return errors.Join(fmt.Errorf("acquire cleanup lock: %w", err), restoreErr)
	}
	if restoreErr != nil {
		return fmt.Errorf("restore viewer lock: %w", restoreErr)
	}
	return nil
}
