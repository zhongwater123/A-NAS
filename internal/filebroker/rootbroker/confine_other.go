//go:build !linux

package rootbroker

import "errors"

func confine(string, string) error {
	return errors.New("confined file workers need Linux Landlock")
}
