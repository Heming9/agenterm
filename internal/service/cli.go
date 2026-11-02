package service

import (
	"agenterm/internal/logging"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"os"
	"time"
)

type serverCommand struct {
	Action  string
	Paths   instancePaths
	Options *runtimeLaunchOptions
	Stdio   bool
}

func parseServerCommand(args []string, output io.Writer) (serverCommand, error) {
	action := "start"
	if len(args) > 0 {
		switch args[0] {
		case "start", "stop", "restart", "status":
			action, args = args[0], args[1:]
		case "help":
			args = []string{"-help"}
		}
	}
	flags := flag.NewFlagSet("agenterm-server", flag.ContinueOnError)
	flags.SetOutput(output)
	options := addRuntimeLaunchFlags(flags)
	instance := flags.String("instance", defaultInstance, "Instance name; separates independent servers")
	stdio := flags.Bool("stdio", false, serverText("mcp.stdio.transport"))
	flags.Usage = func() {
		fmt.Fprintln(output, "Usage: agenterm-server [start|stop|restart|status] [options]")
		fmt.Fprintln(output, "\nCommands:")
		fmt.Fprintln(output, "  start    Start or replace server, retaining runtime and sessions (default)")
		fmt.Fprintln(output, "  stop     Stop server and runtime; clear in-memory state")
		fmt.Fprintln(output, "  restart  Recreate runtime and start server with the supplied configuration")
		fmt.Fprintln(output, "  status   Check server and runtime without starting processes")
		fmt.Fprintln(output, "\nstop and status accept only -instance. Options:")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return serverCommand{}, err
	}
	if flags.NArg() != 0 {
		return serverCommand{}, fmt.Errorf("unexpected argument: %s; use -help for commands", flags.Arg(0))
	}
	paths, err := pathsForInstance(*instance)
	if err != nil {
		return serverCommand{}, err
	}
	options.Fields = make(map[string]bool)
	flags.Visit(func(f *flag.Flag) { options.Fields[f.Name] = true })
	if action == "stop" || action == "status" {
		for name := range options.Fields {
			if name != "instance" {
				return serverCommand{}, fmt.Errorf("%s accepts only -instance", action)
			}
		}
	}
	options.Settings.ServerSocket = paths.Server
	if action == "start" || action == "restart" {
		if err := validateRuntimeLaunch(options); err != nil {
			return serverCommand{}, err
		}
	}
	if action == "restart" {
		if _, err := parsePublicURL(options.Settings.PublicURL, options.Settings.Prefix); err != nil {
			return serverCommand{}, err
		}
	}
	return serverCommand{Action: action, Paths: paths, Options: options, Stdio: *stdio}, nil
}

// ServerMain is the sole public executable entrypoint. Runtime is an internal
// worker launched through an inherited pipe, never a public subcommand.
func ServerMain() {
	if os.Getenv("AGENTERM_INTERNAL_RUNTIME") == "1" {
		if os.Getenv("AGENTERM_RUNTIME_BOOTSTRAP_FD") != "3" {
			log.Fatal("runtime must be started by agenterm-server")
		}
		_ = os.Unsetenv("AGENTERM_INTERNAL_RUNTIME")
		runtimeWorkerMain()
		return
	}
	if err := runServerCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil && !errors.Is(err, context.Canceled) {
		log.Fatal(err)
	}
}

func runServerCommand(args []string, output, help io.Writer) (err error) {
	command, err := parseServerCommand(args, help)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = withRuntimeInstance(err, command.Paths.Name) }()
	if command.Action == "status" {
		return instanceStatus(command.Paths, output)
	}
	if command.Action != "stop" {
		closeLog, err := configureServiceLogging("server", command.Paths.Name)
		if err != nil {
			return err
		}
		defer closeLog()
		defer func() {
			if err != nil {
				slog.Error("server command failed", "action", command.Action, "error_kind", logging.ErrorKind(err))
			}
		}()
	}
	if command.Action == "stop" || command.Action == "restart" {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := stopManagedServices(ctx, command.Paths)
		cancel()
		if err != nil {
			return err
		}
		if command.Action == "stop" {
			fmt.Fprintf(output, "Instance %s stopped\n", command.Paths.Name)
			return nil
		}
	}
	if err := command.Paths.prepare(); err != nil {
		return err
	}
	if command.Stdio {
		output = help
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	config, err := ensureRuntime(ctx, command.Paths.Runtime, command.Options, output)
	cancel()
	if err != nil {
		return err
	}
	slog.Info("runtime adopted", "runtime_pid", config.PID)
	return serverMain(command.Paths, command.Stdio, config)
}

func instanceStatus(paths instancePaths, output io.Writer) error {
	// A status check must not create a directory or launch a process.
	for _, dir := range []string{paths.Root, paths.Dir} {
		info, err := os.Lstat(dir)
		if os.IsNotExist(err) {
			return fmt.Errorf("instance %s: stopped", paths.Name)
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm()&0077 != 0 || !ownsSocketDirectory(info) {
			return fmt.Errorf("instance %s has an unsafe IPC directory", paths.Name)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := newRuntimeClient(paths.Runtime)
	defer c.http.CloseIdleConnections()
	config, err := controlCall[runtimeConfig](ctx, c, "config", struct{}{})
	if runtimeAbsent(err) {
		return fmt.Errorf("instance %s: stopped", paths.Name)
	}
	if err != nil {
		return err
	}
	backend, _, err := paths.currentBackend()
	if err != nil {
		return err
	}
	if backend == "" {
		return fmt.Errorf("instance %s: runtime running (PID %d), server not ready", paths.Name, config.PID)
	}
	if err := waitBackend(ctx, backend); err != nil {
		return fmt.Errorf("instance %s: runtime running (PID %d), server unavailable", paths.Name, config.PID)
	}
	_, err = fmt.Fprintf(output, "Instance %s: running (runtime PID %d)\n", paths.Name, config.PID)
	return err
}
