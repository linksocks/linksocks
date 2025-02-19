package main

import (
	"log"

	"github.com/zetxtech/wssocks/wssocks"
)

func main() {
	cli := wssocks.NewCLI()

	if err := cli.Execute(); err != nil {
		log.Fatal(err)
	}
}
