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
	// state of a person.
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
	a := account{UID: os.Geteuid(), Home: os.Getenv("HOME")}
	if u, err := user.Current(); err == nil {
		a.Name = u.Username
		if a.Home == "" {
			a.Home = u.HomeDir
		}
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

// describeMode reports the current account and the mode recorded at install time.
func describeMode(mode string, a account) string {
	label := "unknown mode"
	switch mode {
	case modeUser:
		label = "user account"
	case modeDedicated:
		label = "dedicated account"
	}
	return fmt.Sprintf("%s: %s (uid %d, home %s)", label, a.Name, a.UID, a.Home)
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
