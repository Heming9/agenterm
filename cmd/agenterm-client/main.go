package main

import (
	"agenterm/internal/client"
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

const (
	progressAuthorize = "[1/5] 等待连接授权"
	progressConnect   = "[4/5] 正在验证邀请并连接服务器…"
	progressConnected = "[5/5] 连接成功"
)

func main() {
	tokenFlag := flag.String("token", "", "connection token (omit to enter interactively)")
	plainFlag := flag.Bool("plain", false, "use plain text output")
	inviteFlag := flag.String("invite", "", "one-time invitation")
	consentConfirmedFlag := flag.Bool("consent-confirmed", false, "connection consent already obtained by the installer")
	flag.Parse()
	token := strings.TrimSpace(*tokenFlag)
	if *inviteFlag != "" {
		endpoint, _, err := invite.Endpoint(*inviteFlag)
		if err != nil {
			log.Fatal(err)
		}
		if !*consentConfirmedFlag {
			fmt.Printf("%s\n⚠️ 远程控制授权\n\n服务器：%s\n连接后，对方将能够以你当前账户的权限控制本机，包括执行命令、访问和修改文件。请确保你信任对方，并了解此次连接的用途。\n\n是否允许连接？[y/N] ", progressAuthorize, strings.TrimSuffix(endpoint, "/invite/redeem"))
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println("已取消连接。")
				return
			}
		}
		fmt.Println(progressConnect)
		token, err = invite.Redeem(context.Background(), *inviteFlag)
		if err != nil {
			log.Fatalf("连接失败（邀请验证）：%v", err)
		}
	}
	if !*plainFlag && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := runTUI(token, *inviteFlag != ""); err != nil {
			log.Fatal(err)
		}
		return
	}
	if token == "" {
		fmt.Print("Connection token: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		token = strings.TrimSpace(line)
	}
	if *inviteFlag == "" {
		fmt.Println("Connecting. Once connected, the server can execute commands with your current user permissions. Close this program to disconnect.")
	}
	if err := client.Run(context.Background(), token, os.Stdout, func() {
		if *inviteFlag != "" {
			fmt.Println(progressConnected + "。关闭客户端即可断开连接。")
		} else {
			fmt.Println("[Agenterm] Connected. Commands from the server will appear here.")
		}
	}); err != nil {
		log.Fatal(err)
	}
}
