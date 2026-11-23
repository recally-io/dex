package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"strconv"
)

const (
	// modeUser runs the work of the machine as the account that installed it: its PATH, its
	// installed tools, its login state, and its files.
	modeUser = "user"
	// modeDedicated runs the work of the machine as a dedicated account that holds no login
	// state of a person. This is what every install before the install user existed used.
	modeDedicated = "dedicated"
)

// account is the Unix account that runs one dex-agent process.
type account struct {
	Name string
	UID  int
	Home string
}

// currentAccount reports the account that runs this process. It reads the account database and
// falls back to the environment, because a service manager does not always set USER.
func currentAccount() account {
	a := account{UID: os.Getuid(), Home: os.Getenv("HOME")}
	if u, err := user.Current(); err == nil {
		a.Name, a.Home = u.Username, u.HomeDir
	} else {
		a.Name = os.Getenv("USER")
	}
	if a.Name == "" {
		a.Name = os.Getenv("LOGNAME")
	}
	if a.Name == "" {
		a.Name = "uid-" + strconv.Itoa(a.UID)
	}
	if a.Home == "" {
		if home, err := os.UserHomeDir(); err == nil {
			a.Home = home
		}
	}
	return a
}

// modeOf reports the mode that a state file records. A state that an earlier agent wrote has no
// mode, and those installs run a dedicated account.
func modeOf(d device) string {
	if d.Mode == "" {
		return modeDedicated
	}
	return d.Mode
}

// runAsUser is the account that the machine reports to Dex as the one that its work runs as, or
// empty for a dedicated account. The install command records the mode, so a dedicated account
// with its state in its own home is never mistaken for the account of a person.
func (d device) runAsUser() string {
	if modeOf(d) != modeUser {
		return ""
	}
	return d.Account
}

// describeMode explains how the work of the machine runs, for the log and for status.
func describeMode(d device) string {
	if d.runAsUser() == "" {
		return fmt.Sprintf("dedicated account %s (uid %d)", d.Account, d.UID)
	}
	return fmt.Sprintf("the account that installed the machine, %s (uid %d, home %s)", d.Account, d.UID, d.Home)
}

// stateError explains a state directory that cannot be read, and separates a machine that was
// never installed from one that this account may not read.
func stateError(state string, err error) error {
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("no machine state in %s: install the machine with the command from Dex", state)
	case errors.Is(err, fs.ErrPermission):
		a := currentAccount()
		return fmt.Errorf("%s is not readable by %s (uid %d): the machine belongs to another account, so this service cannot use it; install it again for %s", state, a.Name, a.UID, a.Name)
	default:
		return err
	}
}
