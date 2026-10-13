// Command deaddrop receives files from a browser over a temporary,
// link-protected web page.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/itsbryanman/DeadDrop/internal/cli"
	"github.com/itsbryanman/DeadDrop/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	inv, err := cli.Parse(args)
	if err != nil {
		var ue *cli.UsageError
		if errors.As(err, &ue) {
			fmt.Fprintf(stderr, "deaddrop: %s\n", ue.Msg)
			fmt.Fprintln(stderr, "Run 'deaddrop --help' for usage.")
			return cli.ExitUsage
		}
		fmt.Fprintf(stderr, "deaddrop: %v\n", err)
		return cli.ExitRuntime
	}
	switch inv.Command {
	case cli.CommandHelp:
		printHelp(stdout, inv.HelpTopic)
		return cli.ExitOK
	case cli.CommandVersion:
		fmt.Fprintf(stdout, "deaddrop %s\n", version.String())
		return cli.ExitOK
	case cli.CommandReceive:
		return receive(inv.Receive, stdout, stderr)
	}
	return cli.ExitUsage
}

func printHelp(w io.Writer, topic string) {
	if topic == "receive" {
		fmt.Fprint(w, cli.ReceiveUsage)
		return
	}
	fmt.Fprint(w, cli.Usage)
}
