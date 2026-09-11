//go:build !darwin && !windows

package appserver

import (
	"context"
	"errors"
)

func chooseWorkspace(context.Context) (string, bool, error) {
	return "", false, errors.New("native workspace picker is currently implemented for macOS and Windows only")
}
