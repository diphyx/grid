package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/diphyx/grid/v2/pkg/wire"
)

// key is `grid key new <name>` and `grid key pub <private file>`.
func key(args []string) int {
	if len(args) != 2 {
		fmt.Fprint(os.Stderr, USAGE)

		return 2
	}
	switch args[0] {
	case "new":
		{
			return newKey(args[1])
		}
	case "pub":
		{
			keypair, loadError := wire.LoadPrivate(args[1])
			if loadError != nil {
				fail(loadError)
			}
			fmt.Println(keypair.Public.String())

			return 0
		}
	default:
		{
			fmt.Fprint(os.Stderr, USAGE)

			return 2
		}
	}
}

// newKey writes <name>.key (the private key, 0600) and <name>.pub, and prints the public key.
// It never overwrites: a key that exists may be trusted somewhere already.
func newKey(name string) int {
	privatePath, publicPath := name+".key", name+".pub"
	for _, path := range []string{privatePath, publicPath} {
		_, statError := os.Stat(path)
		if statError == nil {
			fail(fmt.Errorf("%s exists; not overwritten", path))
		}
		if !errors.Is(statError, os.ErrNotExist) {
			fail(statError)
		}
	}
	keypair, generateError := wire.GenerateKeypair(nil)
	if generateError != nil {
		fail(generateError)
	}
	privateFile, createError := os.OpenFile(privatePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if createError != nil {
		fail(createError)
	}
	_, writeError := fmt.Fprintln(privateFile, keypair.Private.String())
	closeError := privateFile.Close()
	if writeError != nil {
		fail(writeError)
	}
	if closeError != nil {
		fail(closeError)
	}
	publicError := os.WriteFile(publicPath, []byte(keypair.Public.String()+"\n"), 0o644)
	if publicError != nil {
		fail(publicError)
	}
	fmt.Println(keypair.Public.String())

	return 0
}
