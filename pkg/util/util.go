package util

import (
	"errors"
	"io"
)

// RWC composes io.ReadCloser and io.WriteCloser into io.ReadWriteCloser
type RWC struct {
	io.ReadCloser
	io.WriteCloser
}

func (rwc *RWC) Close() error {
	var merr error
	if err := rwc.ReadCloser.Close(); err != nil {
		merr = errors.Join(merr, err)
	}
	if err := rwc.WriteCloser.Close(); err != nil {
		merr = errors.Join(merr, err)
	}
	if merr != nil {
		return merr
	}
	return nil
}
