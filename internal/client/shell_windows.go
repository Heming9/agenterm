package client

import (
	"os"
	"os/exec"
	"unsafe"

	"golang.org/x/sys/windows"
)

func defaultShell() string {
	path := os.Getenv("ComSpec")
	if path == "" {
		path = "cmd.exe"
	}
	return path
}

func parentShellPath() string {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return ""
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	parent := uint32(os.Getppid())
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if entry.ProcessID != parent {
			continue
		}
		name := windows.UTF16ToString(entry.ExeFile[:])
		if !isSupportedShell(name) {
			return ""
		}
		process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, parent)
		if err == nil {
			defer windows.CloseHandle(process)
			buf := make([]uint16, 32768)
			size := uint32(len(buf))
			if windows.QueryFullProcessImageName(process, 0, &buf[0], &size) == nil {
				return windows.UTF16ToString(buf[:size])
			}
		}
		path, _ := exec.LookPath(name)
		return path
	}
	return ""
}
