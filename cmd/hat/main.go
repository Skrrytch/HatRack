// Command hat is the one binary of HatRack: command line, agent and server.
package main

import (
	"os"
	_ "time/tzdata" // time zones for the scratch image, which has none

	"hatrack/internal/cli"
)

func main() {
	os.Exit(cli.NewApp().Execute(os.Args[1:]))
}
