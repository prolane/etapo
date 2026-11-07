// Command sign-release signs release checksums and checks their signatures.
//
//	sign-release keygen ID      create a key pair for ID and print both lines
//	sign-release sign FILE      write FILE.sig, signed with the keys in $RELEASE_SIGNING_KEYS
//	sign-release verify FILE    check FILE.sig against the release keys built into etapo
//
// See docs/release-signing.md for key generation and rotation.
package main

import (
	"fmt"
	"os"
	"strings"

	"etapo/internal/update"
)

func main() {
	if len(os.Args) != 3 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "keygen":
		err = keygen(os.Args[2])
	case "sign":
		err = sign(os.Args[2])
	case "verify":
		err = verify(os.Args[2])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign-release:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: sign-release keygen ID | sign FILE | verify FILE")
	os.Exit(2)
}

func keygen(id string) error {
	k, err := update.GenerateSigningKey(id)
	if err != nil {
		return err
	}
	fmt.Printf("Private key. Add this line to the RELEASE_SIGNING_KEYS secret and keep a copy somewhere safe:\n%s\n\n", update.FormatSigningKey(k))
	fmt.Printf("Public key. Add this line to builtinReleaseKeys in internal/update/keys.go:\n%s\n", update.FormatReleaseKey(k.Public()))
	return nil
}

func sign(path string) error {
	keys, err := update.ParseSigningKeys(os.Getenv("RELEASE_SIGNING_KEYS"))
	if err != nil {
		return fmt.Errorf("RELEASE_SIGNING_KEYS: %w", err)
	}
	sums, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".sig", update.SignChecksums(sums, keys), 0o644); err != nil {
		return err
	}
	ids := make([]string, 0, len(keys))
	for _, k := range keys {
		ids = append(ids, k.ID)
	}
	fmt.Printf("signed %s with %s\n", path, strings.Join(ids, ", "))
	return nil
}

func verify(path string) error {
	sums, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	sigs, err := os.ReadFile(path + ".sig")
	if err != nil {
		return err
	}
	id, err := update.VerifyChecksums(sums, sigs, nil)
	if err != nil {
		return err
	}
	fmt.Printf("%s is signed by the trusted release key %s\n", path, id)
	return nil
}
