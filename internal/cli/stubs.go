package cli

import (
	"fmt"
	"io"
)

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "loop-guard doctor: not implemented yet")
	return exitErr
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "loop-guard init: not implemented yet")
	return exitErr
}

func cmdServe(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fmt.Fprintln(stderr, "loop-guard serve: not implemented yet")
	return exitErr
}
