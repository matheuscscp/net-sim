package pkgio

import (
	"fmt"
	"io"

	"github.com/hashicorp/go-multierror"
)

func Close(closers ...io.Closer) error {
	var err error
	for i, c := range closers {
		if cErr := c.Close(); cErr != nil {
			err = multierror.Append(err, fmt.Errorf("error closing %d-th closer: %w", i, cErr))
		}
	}
	return err
}
