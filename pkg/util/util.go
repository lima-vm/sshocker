package util

import (
	"io"

	"github.com/hashicorp/go-multierror"
)

// RWC composes io.ReadCloser and io.WriteCloser into io.ReadWriteCloser
type RWC struct {
	io.ReadCloser
	io.WriteCloser
}

func (rwc *RWC) Close() error {
	var merr *multierror.Error
	if err := rwc.ReadCloser.Close(); err != nil {
		merr = multierror.Append(merr, err)
	}
	if err := rwc.WriteCloser.Close(); err != nil {
		merr = multierror.Append(merr, err)
	}
	return merr.ErrorOrNil()
}
