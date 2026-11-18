package service

import (
	"agenterm/internal/version"
	"net/http"
)

func acceptClientVersion(w http.ResponseWriter, r *http.Request) bool {
	w.Header().Set(version.Header, version.Protocol)
	w.Header().Set(version.ReleaseHeader, version.Current)
	if err := version.Check(version.Protocol, r.Header.Get(version.Header)); err != nil {
		w.Header().Set("Cache-Control", "no-store")
		http.Error(w, err.Error(), http.StatusUpgradeRequired)
		return false
	}
	return true
}

func clientReleaseVersion(r *http.Request) string {
	if release := metadataHeader(r, version.ReleaseHeader); release != "" {
		return release
	}
	return metadataHeader(r, version.Header)
}
