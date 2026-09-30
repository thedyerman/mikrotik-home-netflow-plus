// Package routeros talks to the RouterOS binary API (ports 8728 / 8729) and
// polls the data the live lane needs.
package routeros

import (
	"bufio"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

// Options configure a connection to the router.
type Options struct {
	Addr     string // host:port
	User     string
	Password string
	TLS      bool
	// Fingerprint is the expected SHA-256 of the router's certificate (hex).
	// When empty, Pin is consulted instead.
	Fingerprint string
	// Pin is called with the fingerprint the router presented when no
	// Fingerprint is configured. It returns an error to refuse the connection
	// (trust on first use is implemented by the caller).
	Pin     func(fingerprint string) error
	Timeout time.Duration
}

// Client is one authenticated API session. It is safe for concurrent use;
// commands run one at a time.
type Client struct {
	mu      sync.Mutex
	conn    net.Conn
	r       *bufio.Reader
	timeout time.Duration
}

// TrapError is an error reply from the router.
type TrapError struct{ Message string }

func (e *TrapError) Error() string { return "routeros: " + e.Message }

// Dial connects and logs in.
func Dial(ctx context.Context, o Options) (*Client, error) {
	if o.Timeout == 0 {
		o.Timeout = 10 * time.Second
	}
	d := net.Dialer{Timeout: o.Timeout}
	conn, err := d.DialContext(ctx, "tcp", o.Addr)
	if err != nil {
		return nil, err
	}
	if o.TLS {
		cfg := &tls.Config{
			InsecureSkipVerify: true, // the certificate is self-signed; it is pinned by fingerprint below
			MinVersion:         tls.VersionTLS12,
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				if len(raw) == 0 {
					return errors.New("routeros: router presented no certificate")
				}
				sum := sha256.Sum256(raw[0])
				fp := hex.EncodeToString(sum[:])
				if want := normFingerprint(o.Fingerprint); want != "" {
					if fp != want {
						return fmt.Errorf("routeros: certificate fingerprint %s does not match the configured one", fp)
					}
					return nil
				}
				if o.Pin != nil {
					return o.Pin(fp)
				}
				return nil
			},
		}
		tc := tls.Client(conn, cfg)
		_ = tc.SetDeadline(time.Now().Add(o.Timeout))
		if err := tc.HandshakeContext(ctx); err != nil {
			conn.Close()
			if strings.Contains(err.Error(), "handshake failure") {
				return nil, fmt.Errorf("routeros: TLS handshake failed (does api-ssl have a certificate assigned?): %w", err)
			}
			return nil, err
		}
		conn = tc
	}
	c := &Client{conn: conn, r: bufio.NewReaderSize(conn, 64<<10), timeout: o.Timeout}
	if _, _, err := c.Run("/login", "=name="+o.User, "=password="+o.Password); err != nil {
		c.Close()
		return nil, fmt.Errorf("routeros: login: %w", err)
	}
	return c, nil
}

func normFingerprint(s string) string {
	return strings.ToLower(strings.NewReplacer(":", "", " ", "").Replace(s))
}

// Close ends the session.
func (c *Client) Close() error { return c.conn.Close() }

// Run sends one command and returns the reply rows plus the attributes of the
// final !done sentence (for example "ret" for count-only queries).
func (c *Client) Run(words ...string) (rows []map[string]string, done map[string]string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = c.conn.SetDeadline(time.Now().Add(c.timeout))
	var buf []byte
	for _, w := range words {
		buf = appendLen(buf, len(w))
		buf = append(buf, w...)
	}
	buf = append(buf, 0)
	if _, err := c.conn.Write(buf); err != nil {
		return nil, nil, err
	}
	var trap *TrapError
	for {
		sentence, err := c.readSentence()
		if err != nil {
			return nil, nil, err
		}
		if len(sentence) == 0 {
			continue
		}
		attrs := map[string]string{}
		for _, w := range sentence[1:] {
			if strings.HasPrefix(w, "=") {
				k, v, _ := strings.Cut(w[1:], "=")
				attrs[k] = v
			}
		}
		switch sentence[0] {
		case "!re":
			rows = append(rows, attrs)
		case "!trap":
			trap = &TrapError{attrs["message"]}
		case "!fatal":
			return nil, nil, &TrapError{strings.Join(sentence[1:], " ")}
		case "!done":
			if trap != nil {
				return nil, nil, trap
			}
			return rows, attrs, nil
		}
	}
}

func (c *Client) readSentence() ([]string, error) {
	var words []string
	for {
		n, err := c.readLen()
		if err != nil {
			return nil, err
		}
		if n == 0 {
			return words, nil
		}
		if n > 1<<20 {
			return nil, errors.New("routeros: oversized word")
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(c.r, b); err != nil {
			return nil, err
		}
		words = append(words, string(b))
	}
}

func (c *Client) readLen() (int, error) {
	b, err := c.r.ReadByte()
	if err != nil {
		return 0, err
	}
	var extra int
	var n int
	switch {
	case b < 0x80:
		return int(b), nil
	case b < 0xC0:
		n, extra = int(b&0x3F), 1
	case b < 0xE0:
		n, extra = int(b&0x1F), 2
	case b < 0xF0:
		n, extra = int(b&0x0F), 3
	default:
		n, extra = 0, 4
	}
	for i := 0; i < extra; i++ {
		x, err := c.r.ReadByte()
		if err != nil {
			return 0, err
		}
		n = n<<8 | int(x)
	}
	return n, nil
}

func appendLen(b []byte, n int) []byte {
	switch {
	case n < 0x80:
		return append(b, byte(n))
	case n < 0x4000:
		return append(b, byte(n>>8)|0x80, byte(n))
	case n < 0x200000:
		return append(b, byte(n>>16)|0xC0, byte(n>>8), byte(n))
	case n < 0x10000000:
		return append(b, byte(n>>24)|0xE0, byte(n>>16), byte(n>>8), byte(n))
	default:
		return append(b, 0xF0, byte(n>>24), byte(n>>16), byte(n>>8), byte(n))
	}
}
