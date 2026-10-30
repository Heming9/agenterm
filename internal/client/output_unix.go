//go:build !windows

package client

import "io"

type plainOutput struct{ io.Writer }

func (plainOutput) Close() error                 { return nil }
func newOutputWriter(w io.Writer) io.WriteCloser { return plainOutput{w} }
