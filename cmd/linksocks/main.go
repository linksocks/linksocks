package main

import (
	"os"

	"github.com/linksocks/linksocks/linksocks"
)

func main() {
	cli := linksocks.NewCLI()

	if err := cli.Execute(); err != nil {
		linksocks.ReportError(err)
		os.Exit(1)
	}
}
