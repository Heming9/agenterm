package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { uiLanguage = "zh-CN"; os.Exit(m.Run()) }
