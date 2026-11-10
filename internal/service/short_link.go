package service

import (
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"time"

	"agenterm/internal/invite"
)

// shortCodeURL returns a scheme-free address using the token’s public endpoint.
func shortCodeURL(token, code string) string {
	endpoint, _, err := invite.Endpoint(token)
	if err != nil {
		return ""
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	u.Path = strings.TrimSuffix(u.Path, "/invite/redeem") + "/c/" + code
	return u.Host + u.EscapedPath()
}

var shortLinkPage = template.Must(template.New("connection").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><meta name="referrer" content="no-referrer"><title>Agenterm 连接信息</title><style>
body{font:16px/1.6 system-ui,sans-serif;max-width:760px;margin:2rem auto;padding:0 1.25rem;color:#172033}h1{font-size:1.6rem}h2{font-size:1.05rem;margin:1.5rem 0 .4rem}pre{background:#f2f4f8;padding:1rem;border-radius:8px;white-space:pre-wrap;overflow-wrap:anywhere;user-select:all}p{color:#4b5563}
</style></head><body><h1>Agenterm 连接信息</h1><p>在需要远程协助的电脑上，复制并运行对应系统的命令。请只将此页面分享给可信任的人。</p><h2>Windows（PowerShell）</h2><pre>{{.Windows}}</pre><h2>macOS / Linux（终端）</h2><pre>{{.Shell}}</pre><h2>连接令牌</h2><pre>{{.Token}}</pre><h2>连接方法</h2><ol><li>在目标电脑上打开 PowerShell（Windows）或终端（macOS / Linux），运行对应系统的命令。</li><li>阅读授权提示，确认允许连接后按提示继续。</li><li>连接后请保持客户端运行。点击“断开连接”或关闭客户端即可结束远程协助。</li><li>如果已安装 Agenterm 客户端，也可以直接粘贴连接令牌连接。</li></ol><h2>有效期</h2><p>{{.Validity}}</p></body></html>`))

func (s *frontend) shortLink(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; frame-ancestors 'none'")
	code, ok := strings.CutPrefix(r.URL.Path, "/c/")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !validInstallCredential.MatchString(code) || len(code) != shortCodeLength {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	entry, err := controlCall[installerInvitation](r.Context(), s.control, "invitation", invitationRequest{Identity: requestIdentity{Header: r.Header, RemoteAddr: r.RemoteAddr}, Secret: code})
	if err != nil {
		writeControlError(w, err)
		return
	}
	shell, windows := installCommands(entry.Token)
	validity := connectionValidity(entry)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if r.Method == http.MethodGet {
		_ = shortLinkPage.Execute(w, struct{ Windows, Shell, Token, Validity string }{windows, shell, entry.Token, validity})
	}
}

// The runtime tracks the first-connection deadline and later offline expiry.
func connectionValidity(entry installerInvitation) string {
	deadline, err := time.Parse(time.RFC3339, entry.TokenExpiresAt)
	if err != nil {
		return "暂时无法获取到期时间，请联系邀请发起人确认。"
	}
	deadlineText := deadline.In(time.FixedZone("北京时间", 8*60*60)).Format("2006-01-02 15:04:05") + "（北京时间）"
	if entry.TokenState == "offline" {
		return "设备已断开，连接令牌和一键命令有效至 " + deadlineText + "；过期后请重新生成连接信息。"
	}
	if entry.TokenState == "initial" {
		return "首次连接前，连接令牌和一键命令有效至 " + deadlineText + "；过期后请重新生成连接信息，已连接设备不受影响。"
	}
	return "暂时无法获取到期时间，请联系邀请发起人确认。"
}
