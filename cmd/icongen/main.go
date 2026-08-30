// Command icongen (re)generates assets/icon.ico from internal/icons.
// Run it whenever the mark in internal/icons/icons.go changes:
//
//	go run ./cmd/icongen
package main

import (
	"log"
	"os"

	"github.com/kms6402/dodaemon/internal/icons"
)

func main() {
	f, err := os.Create("assets/icon.ico")
	if err != nil {
		log.Fatal(err)
	}
	defer f.Close()

	if err := icons.EncodeICO(f, []int{16, 24, 32, 48, 64, 128, 256}); err != nil {
		log.Fatal(err)
	}
	log.Println("wrote assets/icon.ico")
}
