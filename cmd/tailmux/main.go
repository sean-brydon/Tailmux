package main

import (
	"fmt"
	"github.com/sean-brydon/Tailmux/internal/tailmux"
	"os"
)

func main() {
	if err := tailmux.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "tailmux:", err)
		os.Exit(1)
	}
}
