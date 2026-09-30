package routeros

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeRouter speaks just enough of the API protocol to exercise the client.
func fakeRouter(t *testing.T, handle func(words []string) [][]string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		c := &Client{conn: conn, r: bufio.NewReader(conn), timeout: 5 * time.Second}
		for {
			words, err := c.readSentence()
			if err != nil {
				return
			}
			for _, sentence := range handle(words) {
				var buf []byte
				for _, w := range sentence {
					buf = appendLen(buf, len(w))
					buf = append(buf, w...)
				}
				if _, err := conn.Write(append(buf, 0)); err != nil {
					return
				}
			}
		}
	}()
	return ln.Addr().String()
}

func TestClientLoginRowsAndTrap(t *testing.T) {
	long := strings.Repeat("x", 300) // forces a two-byte length prefix
	addr := fakeRouter(t, func(words []string) [][]string {
		switch words[0] {
		case "/login":
			if len(words) != 3 || words[1] != "=name=flowmon" || words[2] != "=password=secret" {
				return [][]string{{"!trap", "=message=invalid user name or password (6)"}, {"!done"}}
			}
			return [][]string{{"!done"}}
		case "/interface/print":
			return [][]string{
				{"!re", "=.id=*2", "=name=ether1", "=rx-byte=100", "=comment=" + long},
				{"!re", "=.id=*C", "=name=LAN", "=rx-byte=200"},
				{"!done"},
			}
		case "/ip/firewall/connection/print":
			return [][]string{{"!done", "=ret=42"}}
		}
		return [][]string{{"!trap", "=message=no such command"}, {"!done"}}
	})

	if _, err := Dial(context.Background(), Options{Addr: addr, User: "flowmon", Password: "wrong"}); err == nil {
		t.Fatal("login with a wrong password succeeded")
	}
}

func TestClientCommands(t *testing.T) {
	long := strings.Repeat("x", 300)
	addr := fakeRouter(t, func(words []string) [][]string {
		switch words[0] {
		case "/login":
			return [][]string{{"!done"}}
		case "/interface/print":
			return [][]string{
				{"!re", "=.id=*2", "=name=ether1", "=rx-byte=100", "=comment=" + long},
				{"!re", "=.id=*C", "=name=LAN", "=rx-byte=200", "=note=a=b"},
				{"!done"},
			}
		case "/ip/firewall/connection/print":
			return [][]string{{"!done", "=ret=42"}}
		}
		return [][]string{{"!trap", "=message=no such item (4)"}, {"!done"}}
	})
	c, err := Dial(context.Background(), Options{Addr: addr, User: "flowmon", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	rows, _, err := c.Run("/interface/print")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0]["name"] != "ether1" || rows[0]["comment"] != long || rows[1][".id"] != "*C" {
		t.Errorf("unexpected rows: %v", rows)
	}
	if rows[1]["note"] != "a=b" {
		t.Errorf("value containing '=' was cut: %q", rows[1]["note"])
	}

	n, err := countConns(c)
	if err != nil || n != 42 {
		t.Errorf("countConns = %d, %v; want 42", n, err)
	}

	// An error reply must come back as a TrapError and leave the session usable.
	_, _, err = c.Run("/bogus")
	var trap *TrapError
	if !errors.As(err, &trap) || !strings.Contains(trap.Message, "no such item") {
		t.Errorf("want a TrapError, got %v", err)
	}
	if rows, _, err := c.Run("/interface/print"); err != nil || len(rows) != 2 {
		t.Errorf("session unusable after a trap: %v", err)
	}
}

func TestLengthEncodingRoundTrip(t *testing.T) {
	for _, n := range []int{0, 1, 0x7F, 0x80, 0x3FFF, 0x4000, 0x1FFFFF, 0x200000, 0xFFFFFFF} {
		r, w := io.Pipe()
		go func() { w.Write(appendLen(nil, n)); w.Close() }()
		c := &Client{r: bufio.NewReader(r)}
		got, err := c.readLen()
		if err != nil || got != n {
			t.Errorf("length %d decoded as %d (%v)", n, got, err)
		}
	}
}

func TestProtoNumber(t *testing.T) {
	for in, want := range map[string]uint8{"tcp": 6, "udp": 17, "icmp": 1, "icmpv6": 58, "gre": 47, "ipsec-esp": 50, "132": 132, "weird": 0} {
		if got := protoNumber(in); got != want {
			t.Errorf("protoNumber(%q) = %d, want %d", in, got, want)
		}
	}
}
