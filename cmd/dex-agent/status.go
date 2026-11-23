package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	json "encoding/json/v2"

	"github.com/recally-io/dex/internal/loginenv"
)

// status reports how this machine runs and whether it holds an enrollment. It prints no secret:
// the state file holds the machine token, and this command reads only the fields it shows.
func status(ctx context.Context, state string, out io.Writer) error {
	a := currentAccount()
	if _, err := os.Stat(state); err != nil { //nolint:gosec // The operator chooses the state directory.
		return stateError(state, err)
	}
	root, err := os.OpenRoot(state)
	if err != nil {
		return stateError(state, err)
	}
	defer root.Close()
	file, err := root.Open("device.json")
	if errors.Is(err, fs.ErrNotExist) {
		fmt.Fprintf(out, "state: %s\n", state)
		fmt.Fprintf(out, "account: %s (uid %d, home %s)\n", a.Name, a.UID, a.Home)
		return fmt.Errorf("no machine state in %s: install the machine with the command from Dex", state)
	}
	if err != nil {
		return stateError(filepath.Join(state, "device.json"), err)
	}
	defer file.Close()
	var d device
	if err := json.UnmarshalRead(io.LimitReader(file, 1<<16), &d); err != nil {
		return errors.New("the machine state file is not valid")
	}
	fmt.Fprintf(out, "mode: %s\n", describeMode(d.Mode, a))
	fmt.Fprintf(out, "state: %s\n", state)
	if d.Origin != "" {
		fmt.Fprintf(out, "dex: %s (environment %s)\n", d.Origin, d.Environment)
	}
	fmt.Fprintf(out, "workspace: %s\n", filepath.Join(state, "workspace"))
	path, pathErr := loginenv.Path(ctx)
	switch {
	case pathErr == nil:
		fmt.Fprintf(out, "path: %s\n", path)
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		fmt.Fprintf(out, "path: %s\n", os.Getenv("PATH"))
		fmt.Fprintf(out, "path source: the login shell (%s), so tools installed for the login account of this machine are missing\n", pathErr)
	}
	return nil
}
