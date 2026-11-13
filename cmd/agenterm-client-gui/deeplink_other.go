//go:build !windows && !darwin

package main

func installDeepLinkHandler() {}

func registerDeepLink() error { return nil }
