// Command imagethat-web serves gocryptfs vaults as a browsable file and media
// viewer over HTTP.
//
// Vaults are unlocked from the browser and held only in memory. Decrypted
// content is never written to disk, and responses carrying it are marked
// no-store so the browser does not cache it either.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/adrianm/imagethat/webserver/server"
)

// version is stamped by the release build via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	var (
		showVersion = flag.Bool("version", false, "print the version and exit")
		root        = flag.String("root", ".", "directory to scan for gocryptfs vaults")
		addr        = flag.String("addr", "127.0.0.1:8347", "address to listen on")
		scanDepth   = flag.Int("scan-depth", 3, "how many directory levels below -root to search for vaults")
		idleTimeout = flag.Duration("idle-timeout", 30*time.Minute, "lock vaults after this much browser inactivity (0 disables)")
		thumbCache  = flag.Int64("thumb-cache", 256, "per-vault encrypted thumbnail cache budget, in MiB")
		tlsCert     = flag.String("tls-cert", "", "TLS certificate file (enables HTTPS)")
		tlsKey      = flag.String("tls-key", "", "TLS key file")
		insecure    = flag.Bool("insecure", false, "allow binding a non-loopback address without TLS")
		ffmpegFlag  = flag.String("ffmpeg", "auto", "ffmpeg binary for server-side video thumbnails: 'auto' searches PATH, 'off' disables, or an explicit path")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println("imagethat-web", version)
		return
	}

	logger := log.New(os.Stderr, "", log.LstdFlags)
	useTLS := *tlsCert != "" && *tlsKey != ""

	if (*tlsCert == "") != (*tlsKey == "") {
		fatal(logger, "-tls-cert and -tls-key must be given together")
	}
	if err := checkExposure(*addr, useTLS, *insecure); err != nil {
		fatal(logger, err.Error())
	}

	ffmpegPath := resolveFFmpeg(*ffmpegFlag, logger)

	srv, err := server.New(server.Config{
		Root:            *root,
		ScanDepth:       *scanDepth,
		IdleTimeout:     *idleTimeout,
		ThumbCacheBytes: *thumbCache << 20,
		TLS:             useTLS,
		FFmpegPath:      ffmpegPath,
		Logger:          logger,
	})
	if err != nil {
		fatal(logger, err.Error())
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Addr:    *addr,
		Handler: srv,
		// No WriteTimeout: streaming a large video over a slow link is a
		// legitimately long response.
		ReadHeaderTimeout: 20 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		fatal(logger, fmt.Sprintf("listen on %s: %v", *addr, err))
	}

	scheme := "http"
	if useTLS {
		scheme = "https"
	}
	logger.Printf("serving vaults under %s at %s://%s", *root, scheme, ln.Addr())

	errCh := make(chan error, 1)
	go func() {
		if useTLS {
			errCh <- httpSrv.ServeTLS(ln, *tlsCert, *tlsKey)
		} else {
			errCh <- httpSrv.Serve(ln)
		}
	}()

	// On shutdown, wipe vault keys before exiting rather than relying on the
	// process teardown to do it.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fatal(logger, err.Error())
		}
	case sig := <-sigCh:
		logger.Printf("received %s, locking vaults and shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
		srv.Close()
	}
}

// checkExposure refuses to serve decrypted vault contents in the clear to a
// network beyond this machine unless the operator insists.
func checkExposure(addr string, useTLS, insecure bool) error {
	if useTLS || insecure {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid -addr %q: %v", addr, err)
	}
	if host == "" {
		return fmt.Errorf("-addr %q binds every interface without TLS; "+
			"pass -tls-cert/-tls-key, or -insecure if the network is trusted", addr)
	}
	ip := net.ParseIP(host)
	if ip != nil && ip.IsLoopback() {
		return nil
	}
	if ip == nil && (host == "localhost") {
		return nil
	}
	return fmt.Errorf("-addr %q is not loopback and TLS is off; this would serve decrypted "+
		"vault contents unencrypted over the network. Pass -tls-cert/-tls-key, or -insecure "+
		"to override", addr)
}

// resolveFFmpeg turns the -ffmpeg flag into a binary path, or "" when video
// thumbnails should stay browser-side.
func resolveFFmpeg(value string, logger *log.Logger) string {
	switch value {
	case "off", "none", "":
		logger.Printf("video thumbnails: server-side disabled (-ffmpeg=%s)", value)
		return ""
	case "auto":
		p, err := exec.LookPath("ffmpeg")
		if err != nil {
			logger.Printf("video thumbnails: no ffmpeg on PATH, falling back to in-browser capture")
			return ""
		}
		logger.Printf("video thumbnails: using ffmpeg at %s", p)
		return p
	default:
		p, err := exec.LookPath(value)
		if err != nil {
			fatal(logger, fmt.Sprintf("-ffmpeg %q: %v", value, err))
		}
		logger.Printf("video thumbnails: using ffmpeg at %s", p)
		return p
	}
}

func fatal(logger *log.Logger, msg string) {
	logger.Printf("error: %s", msg)
	os.Exit(1)
}
