package vault

import (
	"errors"
	"testing"
)

func TestCleanPath(t *testing.T) {
	ok := []struct{ in, want string }{
		{"", ""},
		{"/", ""},
		{".", ""},
		{"foo", "foo"},
		{"/foo", "foo"},
		{"foo/", "foo"},
		{"/foo/bar/", "foo/bar"},
		{"foo//bar", "foo/bar"},
		{"foo/./bar", "foo/bar"},
		{"  /foo/bar  ", "foo/bar"},
		{"foo\\bar", "foo/bar"},
		{"..hidden-prefix.jpg", "..hidden-prefix.jpg"},
		{"file with spaces.jpg", "file with spaces.jpg"},
		{"ünïcode.jpg", "ünïcode.jpg"},
	}
	for _, tc := range ok {
		got, err := CleanPath(tc.in)
		if err != nil {
			t.Errorf("CleanPath(%q): unexpected error %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("CleanPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestCleanPathTraversal is the guard on every path that arrives from an HTTP
// query parameter. Anything containing a ".." component is rejected.
func TestCleanPathTraversal(t *testing.T) {
	escapes := []string{
		"..",
		"../",
		"../etc/passwd",
		"../../../../etc/passwd",
		"/../etc/passwd",
		"foo/../../etc/passwd",
		"foo/../..",
		"..\\..\\windows",
		"a/b/../c",
	}
	for _, in := range escapes {
		if got, err := CleanPath(in); !errors.Is(err, ErrInvalidPath) {
			t.Errorf("CleanPath(%q) = %q, %v; want ErrInvalidPath", in, got, err)
		}
	}
}

func TestCleanPathRejectsNullByte(t *testing.T) {
	if _, err := CleanPath("foo\x00bar"); !errors.Is(err, ErrInvalidPath) {
		t.Errorf("CleanPath with NUL: got %v, want ErrInvalidPath", err)
	}
}

// TestTraversalCannotEscapeVault checks the guard end to end: a traversal
// attempt must not reach a file that exists outside the vault root.
func TestTraversalCannotEscapeVault(t *testing.T) {
	s := openFixture(t)
	for _, attempt := range []string{
		"../../../../etc/passwd",
		"..%2f..%2fetc%2fpasswd",
		"subdir/../../gocryptfs.conf",
	} {
		if _, err := s.ReadFile(attempt); err == nil {
			t.Errorf("ReadFile(%q) unexpectedly succeeded", attempt)
		}
	}
}

// TestConfigFileHidden makes sure gocryptfs.conf is not reachable as content
// even by its exact name.
func TestConfigFileHidden(t *testing.T) {
	s := openFixture(t)
	entries, err := s.ListDir("")
	if err != nil {
		t.Fatalf("ListDir: %v", err)
	}
	for _, e := range entries {
		if e.Name == "gocryptfs.conf" {
			t.Fatal("gocryptfs.conf is listed as vault content")
		}
	}
}

func TestExtensionAndClassify(t *testing.T) {
	cases := []struct {
		name  string
		ext   string
		media MediaType
	}{
		{"a.JPG", "jpg", MediaImage},
		{"a.jpeg", "jpeg", MediaImage},
		{"a.HEIC", "heic", MediaImage},
		{"a.cr3", "cr3", MediaImage},
		{"clip.MP4", "mp4", MediaVideo},
		{"clip.mkv", "mkv", MediaVideo},
		{"notes.txt", "txt", MediaOther},
		{"noext", "", MediaOther},
	}
	for _, tc := range cases {
		if got := Extension(tc.name); got != tc.ext {
			t.Errorf("Extension(%q) = %q, want %q", tc.name, got, tc.ext)
		}
		if got := ClassifyMedia(tc.name); got != tc.media {
			t.Errorf("ClassifyMedia(%q) = %q, want %q", tc.name, got, tc.media)
		}
	}
}

func TestContentType(t *testing.T) {
	cases := map[string]string{
		"a.jpg":  "image/jpeg",
		"a.png":  "image/png",
		"a.mp4":  "video/mp4",
		"a.mov":  "video/quicktime",
		"a.webm": "video/webm",
		"a.zzz":  "application/octet-stream",
	}
	for name, want := range cases {
		if got := ContentType(name); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", name, got, want)
		}
	}
}
