package main

import (
	"log"

	"github.com/zetxtech/linksocks/linksocks"
)

func main() {
	cli := linksocks.NewCLI()

	if err := cli.Execute(); err != nil {
		log.Fatal(err)
	}
}
