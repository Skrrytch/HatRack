// Command hat is the one binary of HatRack: command line, agent and server.
package main

import (
	"os"

	"hatrack/internal/cli"
)

func main() {
	os.Exit(cli.NewApp().Execute(os.Args[1:]))
}
