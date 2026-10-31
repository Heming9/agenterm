package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	cliLanguage = "zh-CN"
	progressConnected = tr("4.4.connected")
	os.Exit(m.Run())
}
