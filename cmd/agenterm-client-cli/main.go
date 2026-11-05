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
	"os/signal"
	"strings"

	"golang.org/x/term"
)

var cliLanguage = i18n.Terminal()

func tr(key string) string { return i18n.Text(cliLanguage, key) }

var progressConnected = tr("4.4.connected")

func main() {
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	options, err := client.ParseOptions(flags, os.Args[1:], true, tr)
	if err != nil {
		log.Fatal(err)
	}
	tokenProvided := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "token" {
			tokenProvided = true
		}
	})
	token := options.Token
	if options.Invitation != "" {
		endpoint, _, err := invite.Endpoint(options.Invitation)
		if err != nil {
			log.Fatal(client.TokenValidationError())
		}
		if !options.ConsentConfirmed {
			fmt.Printf(tr("remote.control.authorization.prompt"), strings.TrimSuffix(endpoint, "/invite/redeem"))
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.ToLower(strings.TrimSpace(answer)) != "y" {
				fmt.Println(tr("connection.cancelled"))
				return
			}
		}
		fmt.Println(tr("3.4.verifying.invitation.and.connecting"))
		token = options.Invitation
	}
	if !options.Plain && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := runTUI(token, options.Invitation != "", tokenProvided); err != nil {
			log.Fatal(err)
		}
		return
	}
	if token == "" && !tokenProvided {
		fmt.Print(tr("connection.token.2"))
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		token = strings.TrimSpace(line)
	}
	if options.Invitation == "" {
		fmt.Println(tr("connecting.once.connected.the.server.can.execute"))
	}
	run := client.RunWithCallbacks
	if options.Invitation != "" {
		run = client.RunInvitationWithCallbacks
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, token, os.Stdout, client.Callbacks{Connected: func() {
		if options.Invitation != "" {
			fmt.Println(progressConnected + tr("close.the.client.to.disconnect"))
		} else {
			fmt.Println(tr("agenterm.connected.commands.from.the.server.will"))
		}
	}}); err != nil {
		log.Fatal(err)
	}
}
