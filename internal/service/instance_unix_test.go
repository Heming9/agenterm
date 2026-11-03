//go:build !windows

package service

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestAutomaticSwitchFromLegacySocketKeepsReplacementReachable(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "ag-link-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	t.Setenv("TMPDIR", dir)
	paths, err := pathsForInstance("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if err := paths.prepare(); err != nil {
		t.Fatal(err)
	}
	controlListener, err := listenUnix(paths.Runtime)
	if err != nil {
		t.Fatal(err)
	}
	state := &runtimeState{}
	controlServer := &http.Server{Handler: state.controlHandler()}
	go controlServer.Serve(controlListener)
	defer controlServer.Close()
	legacy, err := listenUnix(paths.Server)
	if err != nil {
		t.Fatal(err)
	}
	oldContext, cancelOld := context.WithCancel(context.Background())
	defer cancelOld()
	old := &frontend{shutdown: cancelOld}
	oldServer := &http.Server{Handler: old.socketHandler(http.NotFoundHandler())}
	oldDone := make(chan error, 1)
	go func() { oldDone <- serveUntilStopped(oldContext, oldServer, legacy, time.Second) }()
	defer oldServer.Close()
	candidate := paths.backendSocket()
	listener, err := listenUnix(candidate)
	if err != nil {
		t.Fatal(err)
	}
	replacement := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })}
	go replacement.Serve(listener)
	defer replacement.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := waitBackend(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	c := newRuntimeClient(paths.Runtime)
	defer c.http.CloseIdleConnections()
	if err := publishBackend(ctx, paths, candidate, os.Getpid(), c); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-oldDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("legacy server did not drain")
	}
	current, wasLegacy, err := paths.currentBackend()
	if err != nil || wasLegacy || current != candidate {
		t.Fatalf("replacement link lost: %s %v %v", current, wasLegacy, err)
	}
	if err := waitBackend(ctx, paths.Server); err != nil {
		t.Fatalf("closing old socket removed replacement link: %v", err)
	}
}
