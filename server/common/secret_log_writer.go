package common

import "io"

type secretLogWriter struct {
	output io.Writer
}

// NewSecretLogWriter treats each Write as a complete log event.
func NewSecretLogWriter(output io.Writer) io.Writer {
	if _, guarded := output.(*secretLogWriter); guarded {
		return output
	}
	return &secretLogWriter{output: output}
}

func (w *secretLogWriter) Write(input []byte) (int, error) {
	redacted := []byte(RedactSecrets(string(input)))
	n, err := w.output.Write(redacted)
	if n == len(redacted) {
		return len(input), err
	}
	if err == nil {
		err = io.ErrShortWrite
	}
	// A transformed partial write has no reliable corresponding input offset.
	return 0, err
}
