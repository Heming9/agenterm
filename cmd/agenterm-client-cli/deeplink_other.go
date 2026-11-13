//go:build !darwin && !windows && !linux

package main

func registerCLILink() error { return nil }
