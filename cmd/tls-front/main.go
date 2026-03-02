package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"golang.org/x/crypto/acme/autocert"
)

func main() {
	listen := flag.String("listen", ":443", "TLS listen address (e.g. :443)")
	httpChallengeAddr := flag.String("http-addr", ":80", "HTTP address for ACME HTTP-01 challenge (e.g. :80, empty to disable)")
	domainsFlag := flag.String("domains", "", "comma-separated list of domain names for certificates (e.g. example.com,www.example.com)")
	backend := flag.String("backend", "127.0.0.1:4001", "backend TCP address for hypernet-node/libp2p")
	cacheDir := flag.String("cache", "cert-cache", "directory for ACME certificate cache")
	flag.Parse()

	if *domainsFlag == "" {
		fmt.Fprintln(os.Stderr, "tls-front: -domains is required")
		os.Exit(2)
	}

	domains := parseDomains(*domainsFlag)
	if len(domains) == 0 {
		fmt.Fprintln(os.Stderr, "tls-front: no valid domains parsed from -domains")
		os.Exit(2)
	}

	m := &autocert.Manager{
		Prompt:     autocert.AcceptTOS,
		Cache:      autocert.DirCache(*cacheDir),
		HostPolicy: autocert.HostWhitelist(domains...),
	}

	// HTTP server for ACME HTTP-01 challenge.
	if *httpChallengeAddr != "" {
		go func() {
			s := &http.Server{
				Addr:    *httpChallengeAddr,
				Handler: m.HTTPHandler(http.HandlerFunc(httpNotFoundHandler)),
			}
			if err := s.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("tls-front: HTTP challenge server error: %v", err)
			}
		}()
	}

	// TLS listener for raw TCP tunneling to backend.
	baseLn, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("tls-front: listen %s: %v", *listen, err)
	}
	defer baseLn.Close()

	tlsCfg := &tls.Config{
		GetCertificate: m.GetCertificate,
		MinVersion:     tls.VersionTLS12,
	}
	tlsLn := tls.NewListener(baseLn, tlsCfg)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	log.Printf("tls-front: listening on %s for TLS, backend %s, domains=%v", *listen, *backend, domains)
	if *httpChallengeAddr != "" {
		log.Printf("tls-front: ACME HTTP-01 challenge server on %s, cache dir=%s", *httpChallengeAddr, *cacheDir)
	}

	go func() {
		<-sigCh
		log.Printf("tls-front: shutting down on signal")
		_ = tlsLn.Close()
	}()

	for {
		conn, err := tlsLn.Accept()
		if err != nil {
			// If listener is closed due to signal, exit loop.
			if ne, ok := err.(net.Error); ok && !ne.Temporary() {
				break
			}
			if strings.Contains(err.Error(), "use of closed network connection") {
				break
			}
			log.Printf("tls-front: accept error: %v", err)
			continue
		}

		go handleTLSConn(conn, *backend)
	}
}

func parseDomains(s string) []string {
	parts := strings.Split(s, ",")
	var out []string
	for _, p := range parts {
		d := strings.TrimSpace(p)
		if d != "" {
			out = append(out, d)
		}
	}
	return out
}

func httpNotFoundHandler(w http.ResponseWriter, r *http.Request) {
	// Для всех обычных запросов (кроме ACME-ченджей) отдаём 404,
	// так как этот фронт предназначен только для TLS-туннелирования.
	http.NotFound(w, r)
}

func handleTLSConn(tlsConn net.Conn, backendAddr string) {
	defer tlsConn.Close()

	be, err := net.Dial("tcp", backendAddr)
	if err != nil {
		log.Printf("tls-front: backend dial %s failed: %v", backendAddr, err)
		return
	}
	defer be.Close()

	errCh := make(chan error, 2)

	go func() {
		_, err := io.Copy(be, tlsConn)
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(tlsConn, be)
		errCh <- err
	}()

	<-errCh
}

