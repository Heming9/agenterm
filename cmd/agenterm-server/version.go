package main

import (
	"agenterm/internal/version"
	"net/http"
)

func acceptClientVersion(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set(version.Header, version.Current)
	if err := version.Check(version.Current, r.Header.Get(version.Header)); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, err.Error(), http.StatusUpgradeRequired)
		return false
	}
	return true
}
