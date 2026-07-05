package cli

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"reasonix/internal/codegraph"
	"reasonix/internal/config"
	"reasonix/internal/netclient"
)

// codegraphCommand backs `reasonix codegraph` — managing the CodeGraph
// code-intelligence runtime that reasonix otherwise fetches lazily on first use.
func codegraphCommand(args []string) int {
	sub := ""
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "install":
		return codegraphInstall()
	case "sync":
		return codegraphSync(args[1:])
	case "index":
		return codegraphIndex(args[1:])
	case "status", "":
		return codegraphStatus()
	case "help", "-h", "--help":
		codegraphUsage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown codegraph subcommand %q\n\n", sub)
		codegraphUsage()
		return 2
	}
}

func codegraphInstall() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	client, err := netclient.NewHTTPClient(cfg.NetworkProxySpec(), netclient.TransportOptions{})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	p, err := codegraph.InstallWithClient(context.Background(), client, func(m string) { fmt.Println(m) })
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("codegraph ready:", p)
	return 0
}

func codegraphStatus() int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("%-13s %v\n", "enabled:", cfg.Codegraph.Enabled)
	fmt.Printf("%-13s %v\n", "auto_install:", cfg.Codegraph.AutoInstall)
	fmt.Printf("%-13s %s\n", "startup:", cfg.Codegraph.ResolvedTier())
	fmt.Printf("%-13s %s\n", "version:", codegraph.Version)
	fmt.Printf("%-13s %s\n", "cache:", codegraph.CacheDir())
	if p, ok := codegraph.Resolve(cfg.Codegraph.Path); ok {
		fmt.Printf("%-13s %s\n", "resolved:", p)
	} else {
		fmt.Printf("%-13s %s\n", "resolved:", "(not installed — run `reasonix codegraph install`)")
	}
	return 0
}

func codegraphSync(args []string) int {
	return runCodegraph("sync", args)
}

func codegraphIndex(args []string) int {
	return runCodegraph("index", args)
}

func runCodegraph(subcommand string, args []string) int {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	bin, ok := codegraph.Resolve(cfg.Codegraph.Path)
	if !ok {
		fmt.Fprintln(os.Stderr, "codegraph is not installed — run `reasonix codegraph install`")
		return 1
	}
	path := "."
	if len(args) > 0 && args[0] != "" {
		path = args[0]
	}
	cmd := exec.Command(bin, append([]string{subcommand}, path)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "codegraph %s: %v\n", subcommand, err)
		return 1
	}
	return 0
}

func codegraphUsage() {
	fmt.Print(`reasonix codegraph — manage the CodeGraph code-intelligence runtime

Usage:
  reasonix codegraph install             download + cache the runtime
  reasonix codegraph status              show config, cache dir, and resolved launcher
  reasonix codegraph sync [path]         incremental index update after file changes
  reasonix codegraph index [path]        full re-index of all files

CodeGraph is fetched automatically on first use (unless [codegraph].auto_install
is false); this command installs it explicitly or reports where it resolves from.
`)
}
