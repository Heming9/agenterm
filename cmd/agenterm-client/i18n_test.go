package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	cliLanguage = "zh-CN"
	progressConnected = tr("5.5.connected")
	os.Exit(m.Run())
}
