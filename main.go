package main

import (
	"fmt"
	"os"

	"github.com/frenchfaso/Alina/internal/alina"
)

func main() {
	if err := alina.Main(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "alina:", err)
		os.Exit(1)
	}
}
