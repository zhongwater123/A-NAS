//go:build !linux

package filebroker

import "errors"

func confine(string, string) error {
	return errors.New("confined file workers need Linux Landlock")
}
