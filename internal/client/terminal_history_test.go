package client

import (
	"agenterm/internal/protocol"
	"runtime"
	"testing"
)

func TestCancelHistoryWaitLeavesCommandRunning(t *testing.T) {
	peer := terminalLimitPeer(t)
	command := `sleep 0.2; printf once`
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Milliseconds 200; [Console]::Write('once')"
	}
	send := func(m protocol.Message) {
		t.Helper()
		if err := peer.WriteJSON(m); err != nil {
			t.Fatal(err)
		}
	}
	send(protocol.Message{Type: "session_exec", ID: "command", RequestID: "command", Setup: &protocol.SessionSetup{SessionID: "history-cancel"}, Command: command})
	ack := readTerminalLimitMessage(t, peer, "command", "command_accepted")
	send(protocol.Message{Type: "session_wait", ID: "abandoned", SessionID: ack.SessionID, Read: &protocol.SessionReadRequest{CommandID: "command", WaitMS: 20000}})
	send(protocol.Message{Type: "session_read_cancel", ID: "abandoned"})
	send(protocol.Message{Type: "session_wait", ID: "replacement", SessionID: ack.SessionID, Read: &protocol.SessionReadRequest{CommandID: "command", WaitMS: 20000}})
	reply := readTerminalLimitMessage(t, peer, "replacement", "session_result")
	if reply.Error != "" || reply.Result["status"] != "idle" || reply.Result["exit_code"] != float64(0) || reply.Result["output"] != "once" {
		t.Fatalf("command changed after canceling a result waiter: %v", reply)
	}
	send(protocol.Message{Type: "cancel", ID: ack.SessionID})
	readTerminalLimitMessage(t, peer, ack.SessionID, "done")
}
