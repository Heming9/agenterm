package main

import (
	"os"
	"testing"
)

func TestMain(m *testing.M) { _ = os.Setenv("AGENTERM_LANG", "zh-CN"); os.Exit(m.Run()) }
