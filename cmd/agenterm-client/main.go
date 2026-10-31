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

func tr(key string) string { return i18n.Text(cliLanguage, key) }

var progressConnected = tr("5.5.connected")

func main() {
	tokenFlag := flag.String("token", "", tr("connection.token.omit.to.enter.interactively"))
	plainFlag := flag.Bool("plain", false, tr("use.plain.text.output"))
	inviteFlag := flag.String("invite", "", tr("one.time.invitation"))
	consentConfirmedFlag := flag.Bool("consent-confirmed", false, tr("connection.consent.already.obtained.by.the.installer"))
	flag.Parse()
	token := strings.TrimSpace(*tokenFlag)
	if *inviteFlag != "" {
		endpoint, _, err := invite.Endpoint(*inviteFlag)
		if err != nil {
			log.Fatal(err)
		}
		if !*consentConfirmedFlag {
			fmt.Printf(tr("1.5.awaiting.authorization.remote.control.authorization"), strings.TrimSuffix(endpoint, "/invite/redeem"))
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println(tr("connection.cancelled"))
				return
			}
		}
		fmt.Println(tr("4.5.verifying.invitation.and.connecting"))
		token, err = invite.Redeem(context.Background(), *inviteFlag)
		if err != nil {
			log.Fatalf(tr("connection.failed.invitation.verification.v"), err)
		}
	}
	if !*plainFlag && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := runTUI(token, *inviteFlag != ""); err != nil {
			log.Fatal(err)
		}
		return
	}
	if token == "" {
		fmt.Print(tr("connection.token.2"))
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		token = strings.TrimSpace(line)
	}
	if *inviteFlag == "" {
		fmt.Println(tr("connecting.once.connected.the.server.can.execute"))
	}
	if err := client.Run(context.Background(), token, os.Stdout, func() {
		if *inviteFlag != "" {
			fmt.Println(progressConnected + tr("close.the.client.to.disconnect"))
		} else {
			fmt.Println(tr("agenterm.connected.commands.from.the.server.will"))
		}
	}); err != nil {
		log.Fatal(err)
	}
}
