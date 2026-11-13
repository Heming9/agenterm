package main

/*
#cgo LDFLAGS: -framework Cocoa -framework CoreServices
#include <stdlib.h>
void installAgentermURLHandler(void);
int registerAgentermBundle(const char *path);
*/
import "C"

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"unsafe"
)

func registerDeepLink() error {
	C.installAgentermURLHandler()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	bundle, err := prepareMacLinkBundle(exe, dir)
	if err != nil {
		return err
	}

	p := C.CString(bundle)
	defer C.free(unsafe.Pointer(p))
	if status := C.registerAgentermBundle(p); status != 0 {
		return fmt.Errorf("register Agenterm URL handler: %d", int(status))
	}
	return nil
}

// Launch Services needs a bundle declaring the scheme, even when the app was
// started from an independently packaged bundle that lacks URL metadata.
func prepareMacLinkBundle(exe, configDir string) (string, error) {
	bundle := filepath.Join(configDir, "Agenterm", "Agenterm.app")
	binary := filepath.Join(bundle, "Contents", "MacOS", "Agenterm")
	if filepath.Clean(exe) == filepath.Clean(binary) {
		return bundle, nil
	}
	if err := os.MkdirAll(filepath.Dir(binary), 0755); err != nil {
		return "", err
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(binary), ".agenterm-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Chmod(0755); err != nil {
		tmp.Close()
		return "", err
	}
	if err = tmp.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(tmp.Name(), binary); err != nil {
		return "", err
	}
	plist := `<?xml version="1.0" encoding="UTF-8"?><!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd"><plist version="1.0"><dict><key>CFBundleIdentifier</key><string>io.agenterm.client.link</string><key>CFBundleName</key><string>Agenterm</string><key>CFBundleExecutable</key><string>Agenterm</string><key>CFBundlePackageType</key><string>APPL</string><key>CFBundleURLTypes</key><array><dict><key>CFBundleURLSchemes</key><array><string>agenterm</string></array></dict></array></dict></plist>`
	if err = os.WriteFile(filepath.Join(bundle, "Contents", "Info.plist"), []byte(plist), 0644); err != nil {
		return "", err
	}
	if out, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", bundle).CombinedOutput(); err != nil {
		return "", fmt.Errorf("sign link handler: %w: %s", err, out)
	}
	return bundle, nil
}

//export agentermOpenURL
func agentermOpenURL(value *C.char) { receiveDeepLink(C.GoString(value)) }
