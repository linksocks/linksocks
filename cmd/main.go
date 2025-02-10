package main

import (
	"log"

	"wssocks/wssocks"
)

func main() {
	cli := wssocks.NewCLI()

	if err := cli.Execute(); err != nil {
		log.Fatal(err)
	}
}
