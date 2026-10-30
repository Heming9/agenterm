package main

import (
	"agenterm/internal/client"
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
	flag.Parse()
	token := strings.TrimSpace(*tokenFlag)
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
