package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	cliLanguage = "zh-CN"
	progressConnected = tr("[5/5] Connected", "[5/5] 连接成功")
	os.Exit(m.Run())
}
