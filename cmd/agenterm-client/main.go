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
)

func main() {
	tokenFlag := flag.String("token", "", "connection token (omit to enter interactively)")
	inviteFlag := flag.String("invite", "", "one-time invitation")
	flag.Parse()
	token := strings.TrimSpace(*tokenFlag)
	if *inviteFlag != "" {
		endpoint, _, err := invite.Endpoint(*inviteFlag)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("Invitation from %s. The server can run commands with your user permissions.\nAllow this connection? [y/N] ", endpoint)
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(answer)) != "y" {
			fmt.Println("Canceled.")
			return
		}
		token, err = invite.Redeem(context.Background(), *inviteFlag)
		if err != nil {
			log.Fatal(err)
		}
	}
	if token == "" {
		fmt.Print("Connection token: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		token = strings.TrimSpace(line)
	}
	fmt.Println("Connecting. Once connected, the server can execute commands with your current user permissions. Close this program to disconnect.")
	if err := client.Run(context.Background(), token, os.Stdout, func() { fmt.Println("[Agenterm] Connected. Commands from the server will appear here.") }); err != nil {
		log.Fatal(err)
	}
}
