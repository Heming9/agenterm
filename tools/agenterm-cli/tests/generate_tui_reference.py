#!/usr/bin/env python3
"""Regenerate Rust's screen fixtures from the actual Go TUI renderer."""
from pathlib import Path
import os
import subprocess
import tempfile

ROOT = Path(__file__).resolve().parents[3]
GO_TEST = r'''package main
import (
 "encoding/json"
 "os"
 "strings"
 "testing"
 "time"
 "agenterm/internal/client"
 "agenterm/internal/version"
 tea "github.com/charmbracelet/bubbletea"
 "github.com/charmbracelet/lipgloss"
 "github.com/charmbracelet/x/ansi"
 "github.com/muesli/termenv"
)
func TestExportRustUIReference(t *testing.T) {
 lipgloss.SetColorProfile(termenv.Ascii)
 type Screen struct { Language string `json:"language"`; Scenario string `json:"scenario"`; Width int `json:"width"`; Height int `json:"height"`; Rows []string `json:"rows"` }
 result := struct { Version string `json:"version"`; Screens []Screen `json:"screens"` }{Version:version.Current}
 for _,lang:=range []string{"en","zh-CN"} {
  t.Setenv("AGENTERM_LANG",lang)
  cliLanguage=lang;progressConnected=tr("4.4.connected")
  for _,width:=range []int{100,80,40,25,18} {
   for _,scenario:=range []string{"input","mode","server","invalid","connecting","connected","log","expanded","reconnecting","invitation"} {
    m:=newTUI("");m.temporary=true
    m.Update(tea.WindowSizeMsg{Width:width,Height:16})
    switch scenario {
    case "mode":m.input.SetValue("example.com/c/ABC123");m.selectingMode=true
    case "server":m.input.SetValue("ABC123");m.serverChoices=[]string{"https://one.example/agenterm","https://two.example/custom"};m.selectingServer=true;m.serverIndex=1
    case "invalid":m.message=client.TokenValidationError().Error()
    default:
     if scenario!="input" {
      m.address="ws://127.0.0.1:18743/agenterm/client/abc123";m.state="connected"
      latency:=42*time.Millisecond;m.latency=latency
      if scenario=="connecting" {m.state="connecting";m.latency=-1}
      if scenario=="reconnecting" {m.state="reconnecting";m.retryDelay=3*time.Second;m.latency=-1}
      if scenario=="invitation" {m.connectionProgress=true;m.everConnected=false;m.Update(tuiEvent{connected:true})}
      if scenario=="log" || scenario=="expanded" {
       m.Update(tuiEvent{command:&client.CommandEvent{ID:"one",Command:"printf '你好 world\\n'",StartedAt:time.Date(2026,1,1,12,34,56,0,time.UTC)}})
       m.Update(tuiEvent{id:"one",output:[]byte("\r\n你好 world\nsecond line\n\n")})
       m.Update(tuiEvent{command:&client.CommandEvent{ID:"one",Done:true}})
       if scenario=="expanded" {for i:=range m.spans {m.spans[i].expanded=true};m.refreshLog()}
       m.viewport.GotoTop()
      }
     }
    }
    rows:=strings.Split(ansi.Strip(m.View()),"\n")
    for i:=range rows {rows[i]=strings.TrimRight(strings.ReplaceAll(rows[i],"\x00","")," ")}
    for len(rows)<16 {rows=append(rows,"")}
    result.Screens=append(result.Screens,Screen{lang,scenario,width,16,rows})
    m.cancel()
   }
  }
 }
 b,err:=json.MarshalIndent(result,"","  ");if err!=nil {t.Fatal(err)}
 if err=os.WriteFile(os.Getenv("AGENTERM_TUI_REFERENCE"),append(b,'\n'),0600);err!=nil {t.Fatal(err)}
}
'''

def main():
    target = ROOT / 'tools/agenterm-cli/tests/fixtures/go-tui.json'
    handle = tempfile.NamedTemporaryFile(mode='w', prefix='rust_reference_', suffix='_test.go', dir=ROOT/'cmd/agenterm-client-cli', delete=False)
    source = Path(handle.name)
    try:
        handle.write(GO_TEST)
        handle.close()
        subprocess.run(['go', 'test', './cmd/agenterm-client-cli', '-run', '^TestExportRustUIReference$', '-count=1'], cwd=ROOT, env=dict(os.environ, AGENTERM_TUI_REFERENCE=str(target)), check=True)
    finally:
        handle.close()
        source.unlink(missing_ok=True)
    print(target)

if __name__ == '__main__':
    main()
