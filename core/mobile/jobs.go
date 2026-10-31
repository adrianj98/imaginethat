package mobile

import (
	"context"

	"github.com/adrianm/imagethat/core/remote"
	"github.com/adrianm/imagethat/core/xfer"
)

// Job is one long copy or cache fetch. Create it, start the blocking call on
// a worker thread, and poll Done/Total/Retries or Cancel from any other.
//
// Copies and cache fetches go through core/xfer: a read that fails with a
// transient error (dropped connection, server 5xx) is retried with backoff
// and resumes where it stopped, and the result is committed only when whole.
type Job struct {
	ctx      context.Context
	cancel   context.CancelFunc
	progress xfer.Progress
}

// NewJob returns a job ready to pass to a copy or cache call.
func NewJob() *Job {
	ctx, cancel := context.WithCancel(context.Background())
	return &Job{ctx: ctx, cancel: cancel}
}

// Cancel stops the running call, which then fails with a "cancelled:" error
// and leaves nothing behind.
func (j *Job) Cancel() { j.cancel() }

// Done is how many bytes of the current file have been copied.
func (j *Job) Done() int64 { d, _, _ := j.progress.Snapshot(); return d }

// Total is the current file's size (0 until known).
func (j *Job) Total() int64 { _, t, _ := j.progress.Snapshot(); return t }

// Retries counts transient failures recovered from so far.
func (j *Job) Retries() int64 { _, _, r := j.progress.Snapshot(); return r }

func (j *Job) options() xfer.Options {
	j.progress.Reset()
	return xfer.Options{Progress: &j.progress}
}

// CopyFrom copies srcPath in vault src into this vault at destPath. src may be
// a remote or Drive vault: reads that fail are retried and resumed, and the
// file appears at destPath only when it arrived whole. Blocks until done.
func (v *Vault) CopyFrom(job *Job, destPath string, src *Vault, srcPath string) error {
	_, err := v.s.CopyIn(job.ctx, destPath, src.s.Opener(srcPath), job.options())
	v.invalidateThumb(destPath)
	return wrapErr(err)
}

// CopyFile copies a plaintext file from the device's filesystem into this
// vault at destPath, like CopyFrom.
func (v *Vault) CopyFile(job *Job, destPath, filePath string) error {
	_, err := v.s.CopyIn(job.ctx, destPath, xfer.OpenFile(filePath), job.options())
	v.invalidateThumb(destPath)
	return wrapErr(err)
}

// OpenRemoteCached is OpenRemote with its reads kept in the network cache in
// cacheDir (see networkRanges), as ciphertext: read again, or pinned with
// CacheFile, a file comes off disk, also offline.
func OpenRemoteCached(baseURL, vaultID, password, pinnedSHA256, accessPassword, cacheDir string) (*Vault, error) {
	c, err := remote.New(baseURL, remote.Options{PinnedSHA256: pinnedSHA256})
	if err != nil {
		return nil, wrapErr(err)
	}
	ctx := context.Background()
	if accessPassword != "" {
		if err := c.Login(ctx, accessPassword); err != nil {
			return nil, wrapErr(err)
		}
	}
	ranges, err := networkRanges(cacheDir)
	if err != nil {
		return nil, err
	}
	s, err := c.OpenVaultCached(ctx, vaultID, password, ranges)
	if err != nil {
		return nil, wrapErr(err)
	}
	return &Vault{s: s}, nil
}

// CacheFile makes a file readable without the network. A remote vault opened
// with a cache, or a Drive vault, keeps its ciphertext on disk; for other
// vaults this reads the file through once. Blocks until done.
func (v *Vault) CacheFile(job *Job, path string) error {
	return wrapErr(v.s.CacheFile(job.ctx, path, job.options()))
}

// IsCached reports whether the file is held on this device.
func (v *Vault) IsCached(path string) bool { return v.s.IsCached(path) }

// Uncache drops the device's copy of the file.
func (v *Vault) Uncache(path string) error { return wrapErr(v.s.Uncache(path)) }
