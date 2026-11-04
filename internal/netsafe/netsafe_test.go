package netsafe

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"
)

func TestIsBlockedIP(t *testing.T) {
	blocked := []string{
		"0.0.0.0", "127.0.0.1", "127.8.8.8", "10.1.2.3", "172.16.0.1", "172.31.255.255",
		"192.168.0.1", "169.254.169.254", "100.64.0.1", "192.0.0.1", "192.0.2.1",
		"198.18.0.1", "198.51.100.1", "203.0.113.1", "224.0.0.1", "240.0.0.1", "255.255.255.255",
		"::", "::1", "fe80::1", "fc00::1", "fd12:3456::1", "ff02::1",
		"::ffff:127.0.0.1", "::ffff:10.0.0.1", "64:ff9b::7f00:1", "2001:db8::1", "2002:7f00:1::1", "2001::1",
		"::7f00:1", "::a00:1", "::ffff:0:7f00:1", "fec0::1", "192.88.99.1", "3fff::1", "5f00::1",
	}
	for _, s := range blocked {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("test bug: %q does not parse", s)
		}
		if !IsBlockedIP(ip) {
			t.Errorf("IsBlockedIP(%s) = false, want true", s)
		}
	}
	allowed := []string{"8.8.8.8", "1.1.1.1", "93.184.216.34", "172.32.0.1", "2606:4700:4700::1111"}
	for _, s := range allowed {
		if IsBlockedIP(net.ParseIP(s)) {
			t.Errorf("IsBlockedIP(%s) = true, want false", s)
		}
	}
	if !IsBlockedIP(nil) {
		t.Error("IsBlockedIP(nil) must fail closed")
	}
}

func TestValidateCallbackURL(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 2100)
	cases := []struct {
		name      string
		url       string
		allowHTTP bool
		wantErr   bool
	}{
		{"https domain", "https://example.com/hook", false, false},
		{"https with port and query", "https://api.example.com:8443/x?y=1", false, false},
		{"https public ip", "https://8.8.8.8/h", false, false},
		{"surrounding spaces are trimmed", "  https://example.com/h  ", false, false},

		{"empty", "", false, true},
		{"no host", "https://", false, true},
		{"too long", long, false, true},
		{"malformed", "https://exa mple.com", false, true},
		{"http in production", "http://example.com/h", false, true},
		{"ftp", "ftp://example.com/h", false, true},
		{"javascript", "javascript:alert(1)", false, true},
		{"credentials", "https://user:pw@example.com/h", false, true},
		{"localhost", "https://localhost/x", false, true},
		{"subdomain of localhost", "https://app.localhost/x", false, true},
		{"loopback ip", "https://127.0.0.1/x", false, true},
		{"private 10/8", "https://10.0.0.5/x", false, true},
		{"private 192.168", "https://192.168.1.1/", false, true},
		{"private 172.16", "https://172.16.0.1/", false, true},
		{"metadata", "https://169.254.169.254/latest/meta-data", false, true},
		{"ipv6 loopback", "https://[::1]/", false, true},
		{"ipv6 link-local", "https://[fe80::1]/", false, true},
		{"ipv6 unique local", "https://[fd00::1]/", false, true},
		{"ipv4-mapped ipv6 loopback", "https://[::ffff:127.0.0.1]/", false, true},
		{"decimal ip", "https://2130706433/", false, true},
		{"hex ip", "https://0x7f.0.0.1/", false, true},
		{"octal ip", "https://017700000001/", false, true},
		{"out-of-range dotted", "https://999.1.1.1/", false, true},
		{"single label host", "https://intranet/", false, true},
		{"cgnat", "https://100.64.0.1/", false, true},
		{"unspecified", "https://0.0.0.0/", false, true},
		{"zone id", "https://[fe80::1%25eth0]/", false, true},

		{"trailing dots localhost", "https://localhost../x", false, true},

		{"sandbox http domain", "http://example.com/h", true, false},
		{"sandbox http public ip", "http://8.8.8.8/x", true, false},
		{"sandbox https domain", "https://example.com/h", true, false},
		{"sandbox rejects http localhost", "http://localhost:3000/hook", true, true},
		{"sandbox rejects http loopback", "http://127.0.0.1:8080/x", true, true},
		{"sandbox rejects https private", "https://10.0.0.1/x", true, true},
		{"sandbox rejects metadata", "http://169.254.169.254/", true, true},
		{"sandbox rejects ftp", "ftp://example.com/h", true, true},
		{"sandbox rejects credentials", "https://u:p@example.com/x", true, true},
		{"sandbox rejects empty", "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateCallbackURL(tc.url, tc.allowHTTP)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidCallbackURL) {
					t.Fatalf("want ErrInvalidCallbackURL, got %v", err)
				}
				if tc.url != "" && strings.Contains(err.Error(), tc.url) && len(tc.url) > 12 {
					t.Errorf("error must not echo the URL: %v", err)
				}
				if u, perr := url.Parse(strings.TrimSpace(tc.url)); perr == nil && u.Hostname() != "" &&
					strings.Contains(err.Error(), u.Hostname()) {
					t.Errorf("error must not echo the host: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func newCountingServer(hits *int32, status int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(hits, 1)
		w.WriteHeader(status)
	}))
}

func TestSafeClient_StrictBlocksLoopback(t *testing.T) {
	var hits int32
	srv := newCountingServer(&hits, http.StatusOK)
	defer srv.Close()

	_, err := NewSafeClient(5 * time.Second).Get(srv.URL)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("want ErrBlockedAddress, got %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("blocked request must never reach the server, hits=%d", hits)
	}
}

// A hostname that resolves to an internal address must be rejected at
// connect time — this is the DNS-rebinding case: the name looked fine when
// it was saved, but the IP it resolves to now is internal.
func TestSafeClient_StrictBlocksHostnameResolvingToLoopback(t *testing.T) {
	var hits int32
	srv := newCountingServer(&hits, http.StatusOK)
	defer srv.Close()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))

	_, err := NewSafeClient(5 * time.Second).Get("http://localhost:" + port)
	if !errors.Is(err, ErrBlockedAddress) {
		t.Fatalf("want ErrBlockedAddress, got %v", err)
	}
	if atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("blocked request must never reach the server, hits=%d", hits)
	}
}

func TestSafeClient_DoesNotFollowRedirects(t *testing.T) {
	var targetHits int32
	target := newCountingServer(&targetHits, http.StatusOK)
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	resp, err := newClient(5*time.Second, nil).Get(redirector.URL)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Errorf("status = %d, want 302 returned as-is", resp.StatusCode)
	}
	if atomic.LoadInt32(&targetHits) != 0 {
		t.Errorf("redirect target must not be contacted, hits=%d", targetHits)
	}
}

func TestSafeClient_CheckRedirectUsesLastResponse(t *testing.T) {
	c := NewSafeClient(5 * time.Second)
	if err := c.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect = %v, want http.ErrUseLastResponse", err)
	}
}

func TestCheckDialAddress(t *testing.T) {
	cases := []struct {
		addr    string
		blocked bool
	}{
		{"8.8.8.8:443", false},
		{"[2606:4700:4700::1111]:443", false},
		{"127.0.0.1:80", true},
		{"[::1]:80", true},
		{"[fe80::1%eth0]:80", true},
		{"localhost:80", true},
		{"10.0.0.1:80", true},
		{"169.254.169.254:80", true},
		{"not-an-address", true},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			var c syscall.RawConn
			err := checkDialAddress("tcp", tc.addr, c)
			if tc.blocked && !errors.Is(err, ErrBlockedAddress) {
				t.Errorf("want ErrBlockedAddress, got %v", err)
			}
			if !tc.blocked && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestDescribeError(t *testing.T) {
	blockedWrapped := &url.Error{Op: "Get", URL: "http://10.1.2.3/?token=SECRET",
		Err: &net.OpError{Op: "dial", Net: "tcp", Addr: &net.TCPAddr{IP: net.ParseIP("10.1.2.3"), Port: 80}, Err: ErrBlockedAddress}}
	urlErr := &url.Error{Op: "Post", URL: "https://hook.example.com/cb?token=SECRET", Err: errors.New("connection refused")}
	timeout := &url.Error{Op: "Post", URL: "https://hook.example.com/cb?token=SECRET", Err: errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)")}

	cases := []struct {
		name     string
		err      error
		want     string
		contains string
		never    []string
	}{
		{name: "blocked", err: blockedWrapped, want: ErrBlockedAddress.Error(), never: []string{"SECRET", "10.1.2.3"}},
		{name: "url error", err: urlErr, want: "connection refused", never: []string{"SECRET", "hook.example.com"}},
		{name: "timeout", err: timeout, contains: "Client.Timeout", never: []string{"SECRET", "hook.example.com"}},
		{name: "plain", err: errors.New("boom"), want: "boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeError(tc.err)
			if tc.want != "" && got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
			if tc.contains != "" && !strings.Contains(got, tc.contains) {
				t.Errorf("got %q, want it to contain %q", got, tc.contains)
			}
			for _, n := range tc.never {
				if strings.Contains(got, n) {
					t.Errorf("got %q, must not contain %q", got, n)
				}
			}
		})
	}

	long := DescribeError(errors.New(strings.Repeat("x", 1000)))
	if len(long) != 300 {
		t.Errorf("len = %d, want capped at 300", len(long))
	}
}

// The result is stored in a PostgreSQL TEXT column, which rejects invalid
// UTF-8 and NUL bytes; a rejected write would leave the delivery 'sending'
// and get it re-sent forever. So the text must always be storable.
func TestDescribeError_AlwaysStorableText(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		// "é" is 2 bytes and straddles the 300-byte cap.
		{"multibyte straddles cap", strings.Repeat("x", 299) + "é" + "tail", strings.Repeat("x", 299)},
		// "€" is 3 bytes: starts at 298, ends at 300.
		{"3-byte rune straddles cap", strings.Repeat("x", 298) + "€" + "tail", strings.Repeat("x", 298)},
		{"rune ending exactly at cap kept", strings.Repeat("x", 298) + "é" + "tail", strings.Repeat("x", 298) + "é"},
		{"NUL stripped", "conn\x00reset", "connreset"},
		{"invalid UTF-8 dropped", "bad\xff\xfebytes", "badbytes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := DescribeError(errors.New(tc.in))
			if !utf8.ValidString(got) || strings.ContainsRune(got, 0) {
				t.Fatalf("not storable text: %q", got)
			}
			if len(got) > 300 {
				t.Errorf("len = %d, want <= 300", len(got))
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateCallbackURL_BlockedHosts(t *testing.T) {
	tests := []struct {
		name      string
		blocked   []string
		url       string
		allowHTTP bool
		wantErr   bool
	}{
		{"exact host", []string{"pay.example.com"}, "https://pay.example.com/cb", false, true},
		{"subdomain", []string{"example.com"}, "https://api.example.com/cb", false, true},
		{"deep subdomain", []string{"example.com"}, "https://a.b.example.com/cb", false, true},
		{"case-insensitive url", []string{"example.com"}, "https://API.Example.COM/cb", false, true},
		{"case-insensitive list", []string{"EXAMPLE.com"}, "https://api.example.com/cb", false, true},
		{"trailing dot in url", []string{"example.com"}, "https://api.example.com./cb", false, true},
		{"trailing dot in list", []string{"example.com."}, "https://example.com/cb", false, true},
		{"url with port in list", []string{"https://app.example.com:3000/x"}, "https://app.example.com/cb", false, true},
		{"url port in callback", []string{"example.com"}, "https://example.com:8443/cb", false, true},
		{"bare host with port in list", []string{"app.example.com:3000"}, "https://app.example.com/cb", false, true},
		{"unrelated suffix not blocked", []string{"example.com"}, "https://evilexample.com/cb", false, false},
		{"parent of blocked not blocked", []string{"app.example.com"}, "https://example.com/cb", false, false},
		{"sibling not blocked", []string{"app.example.com"}, "https://other.example.com/cb", false, false},
		{"sandbox http is blocked too", []string{"example.com"}, "http://api.example.com/cb", true, true},
		{"ip literals and localhost ignored in list", []string{"127.0.0.1", "localhost", "", "  "}, "https://merchant.example.org/cb", false, false},
		{"empty list blocks nothing", nil, "https://example.com/cb", false, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(func() { SetBlockedHosts(nil) })
			SetBlockedHosts(tc.blocked)
			err := ValidateCallbackURL(tc.url, tc.allowHTTP)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				if !errors.Is(err, ErrInvalidCallbackURL) {
					t.Errorf("err = %v, want ErrInvalidCallbackURL", err)
				}
				if strings.Contains(err.Error(), "example") {
					t.Errorf("error must not echo the URL or host: %q", err.Error())
				}
			}
		})
	}
}

func TestSetBlockedHosts_ClearingAndReplacing(t *testing.T) {
	t.Cleanup(func() { SetBlockedHosts(nil) })
	const u = "https://api.example.com/cb"

	SetBlockedHosts([]string{"example.com"})
	if ValidateCallbackURL(u, false) == nil {
		t.Fatal("want blocked")
	}
	SetBlockedHosts([]string{"other.org"})
	if err := ValidateCallbackURL(u, false); err != nil {
		t.Fatalf("a new list replaces the old one, got %v", err)
	}
	SetBlockedHosts([]string{"example.com"})
	SetBlockedHosts([]string{})
	if err := ValidateCallbackURL(u, false); err != nil {
		t.Fatalf("empty list clears, got %v", err)
	}
}

func TestSetBlockedHosts_ConcurrentUse(t *testing.T) {
	t.Cleanup(func() { SetBlockedHosts(nil) })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); SetBlockedHosts([]string{"example.com"}) }()
		go func() { defer wg.Done(); _ = ValidateCallbackURL("https://api.example.com/cb", false) }()
	}
	wg.Wait()
}
