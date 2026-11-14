package elevation

import (
	"os"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// shellExecuteInfo mirrors SHELLEXECUTEINFOW, including its pointer alignment.
type shellExecuteInfo struct {
	size       uint32
	mask       uint32
	window     windows.Handle
	verb       *uint16
	file       *uint16
	parameters *uint16
	directory  *uint16
	show       int32
	instance   windows.Handle
	idList     uintptr
	class      *uint16
	classKey   windows.Handle
	hotKey     uint32
	icon       windows.Handle
	process    windows.Handle
}

var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

// Relaunch leaves the original process in place if UAC is cancelled or
// Windows cannot launch the elevated process. On success it waits for the child
// so callers such as the one-click installer retain their normal lifetime.
func Relaunch() (bool, int) {
	if windows.GetCurrentProcessToken().IsElevated() {
		return false, 0
	}
	executable, err := os.Executable()
	if err != nil {
		return false, 0
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return false, 0
	}
	args := make([]string, 0, len(os.Args)-1)
	for _, arg := range os.Args[1:] {
		args = append(args, windows.EscapeArg(arg))
	}
	verb, _ := windows.UTF16PtrFromString("runas")
	file, err := windows.UTF16PtrFromString(executable)
	if err != nil {
		return false, 0
	}
	parameters, err := windows.UTF16PtrFromString(strings.Join(args, " "))
	if err != nil {
		return false, 0
	}
	directory, err := windows.UTF16PtrFromString(workingDir)
	if err != nil {
		return false, 0
	}
	info := shellExecuteInfo{
		size: uint32(unsafe.Sizeof(shellExecuteInfo{})),
		mask: 0x00000040, // SEE_MASK_NOCLOSEPROCESS
		verb: verb, file: file, parameters: parameters, directory: directory,
		show: windows.SW_SHOWNORMAL,
	}
	result, _, _ := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return false, 0
	}
	if info.process == 0 {
		return true, 0
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return true, 1
	}
	var code uint32
	if err := windows.GetExitCodeProcess(info.process, &code); err != nil {
		return true, 1
	}
	return true, int(code)
}
