package service

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	_ = os.Setenv("AGENTERM_LANG", "zh-CN")
	code := m.Run()
	if rustClientDir != "" {
		_ = os.RemoveAll(rustClientDir)
	}
	os.Exit(code)
}
