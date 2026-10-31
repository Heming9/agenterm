package main

import (
	"agenterm/internal/client"
	"agenterm/internal/i18n"
	"agenterm/internal/invite"
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strings"

	"golang.org/x/term"
)

var cliLanguage = i18n.Terminal()

func tr(en, zh string) string { return i18n.Text(cliLanguage, en, zh) }

var progressConnected = tr("[5/5] Connected", "[5/5] 连接成功")

func main() {
	tokenFlag := flag.String("token", "", tr("connection token (omit to enter interactively)", "连接 Token（省略则交互输入）"))
	plainFlag := flag.Bool("plain", false, tr("use plain text output", "使用纯文本输出"))
	inviteFlag := flag.String("invite", "", tr("one-time invitation", "一次性邀请"))
	consentConfirmedFlag := flag.Bool("consent-confirmed", false, tr("connection consent already obtained by the installer", "安装器已取得连接授权"))
	flag.Parse()
	token := strings.TrimSpace(*tokenFlag)
	if *inviteFlag != "" {
		endpoint, _, err := invite.Endpoint(*inviteFlag)
		if err != nil {
			log.Fatal(err)
		}
		if !*consentConfirmedFlag {
			fmt.Printf(tr("[1/5] Awaiting authorization\n⚠️ Remote control authorization\n\nServer: %s\nThe server can control this computer with your current account permissions, including executing commands and accessing or modifying files. Make sure you trust the other party and understand the purpose of this connection.\n\nAllow connection? [y/N] ", "[1/5] 等待连接授权\n⚠️ 远程控制授权\n\n服务器：%s\n连接后，对方将能够以你当前账户的权限控制本机，包括执行命令、访问和修改文件。请确保你信任对方，并了解此次连接的用途。\n\n是否允许连接？[y/N] "), strings.TrimSuffix(endpoint, "/invite/redeem"))
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println(tr("Connection cancelled.", "已取消连接。"))
				return
			}
		}
		fmt.Println(tr("[4/5] Verifying invitation and connecting…", "[4/5] 正在验证邀请并连接服务器…"))
		token, err = invite.Redeem(context.Background(), *inviteFlag)
		if err != nil {
			log.Fatalf(tr("Connection failed (invitation verification): %v", "连接失败（邀请验证）：%v"), err)
		}
	}
	if !*plainFlag && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := runTUI(token, *inviteFlag != ""); err != nil {
			log.Fatal(err)
		}
		return
	}
	if token == "" {
		fmt.Print(tr("Connection token: ", "连接 Token："))
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		token = strings.TrimSpace(line)
	}
	if *inviteFlag == "" {
		fmt.Println(tr("Connecting. Once connected, the server can execute commands with your current user permissions. Close this program to disconnect.", "正在连接。连接后，服务器可使用当前用户权限执行命令。关闭程序即可断开连接。"))
	}
	if err := client.Run(context.Background(), token, os.Stdout, func() {
		if *inviteFlag != "" {
			fmt.Println(progressConnected + tr(". Close the client to disconnect.", "。关闭客户端即可断开连接。"))
		} else {
			fmt.Println(tr("[Agenterm] Connected. Commands from the server will appear here.", "[Agenterm] 已连接。服务器的命令将显示在这里。"))
		}
	}); err != nil {
		log.Fatal(err)
	}
}
