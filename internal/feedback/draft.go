// Package feedback builds a preview for a public issue the user submits themselves.
package feedback

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	Repository    = "recally-io/dex"
	IssueURL      = "https://github.com/" + Repository + "/issues/new"
	PublicWarning = Repository + " is public. Remove secrets and private details before posting."
)

type Draft struct{ Title, Description string }

// Build accepts only the user's two fields. The limits keep channel previews in one message.
func Build(title, description string) (Draft, error) {
	title = strings.TrimSpace(title)
	description = strings.TrimSpace(description)
	if title == "" || strings.ContainsAny(title, "\r\n") || utf8.RuneCountInString(title) > 120 {
		return Draft{}, errors.New("enter a title on one line (up to 120 characters)")
	}
	if description == "" || utf8.RuneCountInString(description) > 1500 {
		return Draft{}, errors.New("enter a description (up to 1500 characters)")
	}
	return Draft{Title: title, Description: description}, nil
}
