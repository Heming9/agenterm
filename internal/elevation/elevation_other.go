//go:build !windows

package elevation

func Relaunch() (bool, int) { return false, 0 }
