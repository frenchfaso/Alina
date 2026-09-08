package main

import (
	"os"

	"github.com/frenchfaso/Alina/internal/alina"
)

func main() {
	if err := alina.Main(os.Args[1:]); err != nil {
		alina.WriteError(os.Stderr, err)
		os.Exit(1)
	}
}
