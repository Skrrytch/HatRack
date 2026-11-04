package modules

import (
	"io"
	"os"
	"os/exec"
)

// maxOutput is how much of a hook's output is kept for error messages and
// answers.
const maxOutput = 64 << 10

// runCaptured runs cmd and returns what it wrote to stdout and stderr. The
// output goes to temporary files, not pipes: a script that starts a program
// in the background ("firefox &") hands its stdout on to that program, and
// with a pipe Wait would block until the program ends.
func runCaptured(cmd *exec.Cmd) (stdout, stderr []byte, err error) {
	outF, err := os.CreateTemp("", "hatrack-out-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(outF.Name())
	defer outF.Close()
	errF, err := os.CreateTemp("", "hatrack-err-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.Remove(errF.Name())
	defer errF.Close()
	cmd.Stdout, cmd.Stderr = outF, errF
	runErr := cmd.Run()
	read := func(f *os.File) []byte {
		f.Seek(0, io.SeekStart)
		b, _ := io.ReadAll(io.LimitReader(f, maxOutput))
		return b
	}
	return read(outF), read(errF), runErr
}
