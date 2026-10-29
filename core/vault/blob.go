package vault

import (
	"bytes"
	"fmt"

	"github.com/adrianm/imagethat/core/internal/gocryptfs/contentenc"
)

// SealBlob encrypts an in-memory buffer in the gocryptfs file format (18-byte
// header followed by encrypted 4 KiB blocks) under this session's content key.
//
// It exists for data derived from vault content that must persist outside the
// vault, such as an on-disk thumbnail cache: the result is unreadable without
// the vault password, exactly like the files themselves. An empty input seals
// to an empty output, matching how gocryptfs stores an empty file.
func (s *Session) SealBlob(plaintext []byte) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.Grow(contentenc.HeaderLen + len(plaintext) + len(plaintext)/4096*32 + 32)
	if _, err := s.encryptStream(&buf, bytes.NewReader(plaintext)); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// OpenBlob decrypts a buffer produced by SealBlob (or any gocryptfs ciphertext
// file read whole). Empty input yields empty output.
func (s *Session) OpenBlob(ciphertext []byte) ([]byte, error) {
	if err := s.checkOpen(); err != nil {
		return nil, err
	}
	if len(ciphertext) == 0 {
		return []byte{}, nil
	}
	if len(ciphertext) < contentenc.HeaderLen {
		return nil, fmt.Errorf("%w: blob too short for header (%d bytes)", ErrCrypto, len(ciphertext))
	}
	header, err := contentenc.ParseHeader(ciphertext[:contentenc.HeaderLen])
	if err != nil {
		return nil, fmt.Errorf("%w: parse header: %v", ErrCrypto, err)
	}
	data := ciphertext[contentenc.HeaderLen:]
	cipherBS := int(s.contentEnc.CipherBS())
	plainSize := s.contentEnc.CipherSizeToPlainSize(uint64(len(ciphertext)))
	out := make([]byte, 0, plainSize)
	var blockNo uint64
	for off := 0; off < len(data); off += cipherBS {
		end := min(off+cipherBS, len(data))
		plain, err := s.decryptBlock(data[off:end], blockNo, header.ID)
		if err != nil {
			wipe(out)
			return nil, fmt.Errorf("decrypt block %d: %w", blockNo, err)
		}
		out = append(out, plain...)
		wipe(plain)
		blockNo++
	}
	return out, nil
}
