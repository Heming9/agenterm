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
	"strconv"
	"strings"
	"time"

	"golang.org/x/term"
)

var cliLanguage = i18n.Terminal()

func tr(key string) string { return i18n.Text(cliLanguage, key) }

var progressConnected = tr("4.4.connected")

func main() {
	args, err := deepLinkArgs(os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
	// Link launches already have a handler; avoid replacing it while it is in use.
	if !isCLILinkLaunch(os.Args[1:]) {
		if err := registerCLILink(); err != nil {
			fmt.Fprintf(os.Stderr, "Agenterm link registration: %v\n", err)
		}
	}
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	options, err := client.ParseOptions(flags, args, true, tr)
	if err != nil {
		log.Fatal(err)
	}
	tokenProvided := false
	modeProvided := false
	flags.Visit(func(f *flag.Flag) {
		if f.Name == "token" {
			tokenProvided = true
		}
		if f.Name == "connection-mode" {
			modeProvided = true
		}
	})
	input := bufio.NewReader(os.Stdin)
	token := options.Token
	if options.Invitation != "" {
		endpoint, _, err := invite.Endpoint(options.Invitation)
		if err != nil {
			log.Fatal(client.TokenValidationError())
		}
		if !options.ConsentConfirmed {
			fmt.Printf(tr("remote.control.authorization.prompt"), strings.TrimSuffix(endpoint, "/invite/redeem"))
			choice := authorizationChoice(input, os.Stdout)
			if choice == 0 {
				fmt.Println(tr("connection.cancelled"))
				return
			}
			options.ConnectionMode = client.ConnectionModePersistent
			if choice == 1 {
				options.ConnectionMode = client.ConnectionModeTemporary
			}
		}
		fmt.Println(tr("3.4.verifying.invitation.and.connecting"))
		token = options.Invitation
	}
	if !options.Plain && term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) {
		if err := runTUI(token, options.Invitation != "", tokenProvided, options.ConnectionMode == client.ConnectionModeTemporary, modeProvided); err != nil {
			log.Fatal(err)
		}
		return
	}
	if token == "" && !tokenProvided {
		fmt.Print(tr("connection.token.2"))
		line, err := input.ReadString('\n')
		if err != nil && err != io.EOF {
			log.Fatal(err)
		}
		token = strings.TrimSpace(line)
	}
	if options.Invitation == "" && client.IsShortCode(token) {
		token, err = selectPlainServer(input, os.Stdout, token)
		if err != nil {
			log.Fatal(err)
		}
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
	serverAddress, _, _ := client.ConnectionAddress(token)
	idleTimeout := time.Duration(0)
	if options.ConnectionMode == client.ConnectionModeTemporary {
		idleTimeout = 5 * time.Minute
	}
	if err := run(ctx, token, os.Stdout, client.Callbacks{IdleTimeout: idleTimeout, IdleDisconnected: func() { fmt.Println(tr("connection.idle.disconnected")) }, Redeemed: func(token string) {
		if target, _, err := client.ConnectionAddress(token); err == nil {
			serverAddress = target
		}
	}, Connected: func() {
		_ = client.RememberServer(serverAddress)
		if options.Invitation != "" {
			fmt.Println(progressConnected + tr("close.the.client.to.disconnect"))
		} else {
			fmt.Println(tr("agenterm.connected.commands.from.the.server.will"))
		}
	}}); err != nil {
		log.Fatal(err)
	}
}

func selectPlainServer(input *bufio.Reader, output io.Writer, code string) (string, error) {
	servers, err := client.ServerHistory()
	if err != nil {
		return "", fmt.Errorf("%s: %w", tr("history.server.failed"), err)
	}
	if len(servers) == 0 {
		return "", fmt.Errorf("%s", tr("history.server.empty"))
	}
	fmt.Fprintln(output, tr("select.history.server"))
	for i, server := range servers {
		fmt.Fprintf(output, "%d. %s\n", i+1, server)
	}
	fmt.Fprint(output, tr("history.server.plain.prompt"))
	answer, err := input.ReadString('\n')
	if err != nil && err != io.EOF {
		return "", err
	}
	answer = strings.TrimSpace(answer)
	index, err := strconv.Atoi(answer)
	if err != nil || index < 1 || index > len(servers) {
		return "", fmt.Errorf("%s", tr("history.server.choose"))
	}
	return client.ShortCodeForServer(servers[index-1], code)
}
