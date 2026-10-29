package mobile

import (
	"errors"
	"io"
	"sync"

	"github.com/adrianm/imagethat/core/vault"
)

var errAborted = errors.New("mobile: write aborted")

// FileWriter streams plaintext into a new vault file. Session.Create runs in
// a goroutine fed through a pipe, so memory use is bounded regardless of file
// size. Call Close to commit or Abort to discard.
type FileWriter struct {
	pw   *io.PipeWriter
	done chan struct{}
	once sync.Once
	err  error // result of Create, valid after done is closed
}

func newFileWriter(s *vault.Session, path string) *FileWriter {
	pr, pw := io.Pipe()
	w := &FileWriter{pw: pw, done: make(chan struct{})}
	go func() {
		_, err := s.Create(path, pr)
		w.err = err
		// Unblock a writer still pushing data if Create bailed out early.
		if err == nil {
			err = io.EOF
		}
		pr.CloseWithError(err)
		close(w.done)
	}()
	return w
}

// Write appends p to the file.
func (w *FileWriter) Write(p []byte) error {
	if _, err := w.pw.Write(p); err != nil {
		select {
		case <-w.done:
			if w.err != nil {
				return wrapErr(w.err)
			}
		default:
		}
		return wrapErr(err)
	}
	return nil
}

// Close finishes the file and commits it to the vault.
func (w *FileWriter) Close() error {
	w.once.Do(func() { w.pw.Close() })
	<-w.done
	return wrapErr(w.err)
}

// Abort discards the file. Create never commits when its input fails, so no
// partial entry or temp file is left and a pre-existing file at the path is
// untouched. Abort after Close is a no-op.
func (w *FileWriter) Abort() error {
	w.once.Do(func() { w.pw.CloseWithError(errAborted) })
	<-w.done
	return nil
}
