package protocol

import (
	"agenterm/internal/i18n"
	"encoding/base64"
	"encoding/binary"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	PathPrefix = "/agenterm"
	ClientPath = PathPrefix + "/client"
	// TokenSecretSize is the number of cryptographically random credential bytes.
	TokenSecretSize = 16
	tokenPrefix     = "agenterm:"
	tokenIPv4       = 4
	tokenIPv6       = 8
	tokenFormat     = 16 // Namespaced routes; older token formats are unsupported.
	tokenCustomPath = 32 // Length-prefixed URL prefix before the address data.
)

const HostnameHeader = "X-Agenterm-Hostname"
const PlatformHeader = "X-Agenterm-Platform"

// Advertised by servers that send Ping frames and latency measurements.
const HeartbeatHeader = "X-Agenterm-Heartbeat"
const HeartbeatInterval = 20 * time.Second
const HeartbeatTimeout = 60 * time.Second
const WriteTimeout = 10 * time.Second

type Token struct {
	URL    string
	Secret string
}

func Encode(t Token) (string, error) {
	u, err := parseTokenURL(t.URL)
	if err != nil {
		return "", err
	}
	secret, err := base64.RawURLEncoding.DecodeString(t.Secret)
	if err != nil || len(secret) != TokenSecretSize {
		return "", i18n.NewError("invalid.token.expected.a.d.byte.secret", TokenSecretSize)
	}
	// Payload: scheme/address flags, raw secret, then address data.
	// IP addresses use a two-byte port (0 = absent) and 4 or 16 address bytes.
	// Domain names and scoped IPv6 addresses use the full host as text.
	b := make([]byte, 1, 1+TokenSecretSize+len(u.Host))
	b[0] = tokenFormat | 1
	if u.Scheme == "wss" {
		b[0] = tokenFormat | 2
	}
	b = append(b, secret...)
	prefix := strings.TrimSuffix(u.Path, "/client")
	if prefix != PathPrefix {
		b[0] |= tokenCustomPath
		b = binary.BigEndian.AppendUint16(b, uint16(len(prefix)))
		b = append(b, prefix...)
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.Zone() == "" {
		var port uint64
		if u.Port() != "" {
			port, err = strconv.ParseUint(u.Port(), 10, 16)
			if err != nil || port == 0 {
				return "", i18n.NewError("invalid.token.port.must.be.between.1.and")
			}
		}
		if ip.Is4() {
			b[0] |= tokenIPv4
		} else {
			b[0] |= tokenIPv6
		}
		b = binary.BigEndian.AppendUint16(b, uint16(port))
		b = append(b, ip.AsSlice()...)
	} else {
		b = append(b, u.Host...)
	}
	return tokenPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}
func Decode(s string) (Token, error) {
	var t Token
	payload, ok := strings.CutPrefix(s, tokenPrefix)
	if !ok {
		return t, i18n.NewError("invalid.token.missing.s.prefix", tokenPrefix)
	}
	b, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return t, i18n.NewError("invalid.token.w", err)
	}
	if len(b) <= 1+TokenSecretSize {
		return t, i18n.NewError("invalid.token.incomplete.payload")
	}
	if b[0]&tokenFormat == 0 {
		return t, i18n.NewError("invalid.token.unsupported.format.update.server.and.client")
	}
	scheme := "ws"
	switch b[0] & 3 {
	case 1:
	case 2:
		scheme = "wss"
	default:
		return t, i18n.NewError("invalid.token.unsupported.format")
	}
	address := b[1+TokenSecretSize:]
	prefix := PathPrefix
	if b[0]&tokenCustomPath != 0 {
		if len(address) < 2 {
			return t, i18n.NewError("invalid.url.prefix")
		}
		size := int(binary.BigEndian.Uint16(address[:2]))
		if size > len(address)-2 {
			return t, i18n.NewError("invalid.url.prefix")
		}
		prefix = string(address[2 : 2+size])
		normalized, err := NormalizePathPrefix(prefix)
		if err != nil || normalized != prefix {
			return t, i18n.NewError("invalid.url.prefix")
		}
		address = address[2+size:]
	}
	host := string(address)
	switch b[0] &^ (3 | tokenFormat | tokenCustomPath) {
	case 0:
	case tokenIPv4, tokenIPv6:
		addressSize := 4
		if b[0]&tokenIPv6 != 0 {
			addressSize = 16
		}
		if len(address) != 2+addressSize {
			return t, i18n.NewError("invalid.token.malformed.ip.address")
		}
		ip, _ := netip.AddrFromSlice(address[2:])
		port := binary.BigEndian.Uint16(address[:2])
		host = ip.String()
		if port != 0 {
			host = net.JoinHostPort(host, strconv.Itoa(int(port)))
		} else if ip.Is6() {
			host = "[" + host + "]"
		}
	default:
		return t, i18n.NewError("invalid.token.unsupported.address.format")
	}
	rawURL := (&url.URL{Scheme: scheme, Host: host, Path: prefix + "/client"}).String()
	u, err := parseTokenURL(rawURL)
	if err != nil {
		return t, err
	}
	if u.Host != host {
		return t, i18n.NewError("invalid.token.malformed.host")
	}
	return Token{URL: rawURL, Secret: base64.RawURLEncoding.EncodeToString(b[1 : 1+TokenSecretSize])}, nil
}

func parseTokenURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Hostname() == "" ||
		u.User != nil || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, i18n.NewError("invalid.token.expected.a.websocket.origin.with.s", ClientPath)
	}
	prefix, ok := strings.CutSuffix(u.Path, "/client")
	normalized, err := NormalizePathPrefix(prefix)
	if !ok || err != nil || prefix != normalized {
		return nil, i18n.NewError("invalid.token.expected.a.websocket.origin.with.s", "<url-prefix>/client")
	}
	return u, nil
}

// NormalizePathPrefix accepts a clean URL path using unreserved ASCII characters.
// An empty string or slash selects root routes. A trailing slash is optional.
func NormalizePathPrefix(prefix string) (string, error) {
	if prefix == "" || prefix == "/" {
		return "", nil
	}
	if !strings.HasPrefix(prefix, "/") || len(prefix) > 1024 {
		return "", i18n.NewError("invalid.url.prefix")
	}
	prefix = strings.TrimSuffix(prefix, "/")
	for _, segment := range strings.Split(prefix[1:], "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", i18n.NewError("invalid.url.prefix")
		}
		for _, c := range segment {
			if !(c >= 'A' && c <= 'Z' || c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || strings.ContainsRune("-._~", c)) {
				return "", i18n.NewError("invalid.url.prefix")
			}
		}
	}
	return prefix, nil
}

type Message struct {
	Type        string `json:"type"`
	ID          string `json:"id,omitempty"`
	Command     string `json:"command,omitempty"`
	Interactive bool   `json:"interactive,omitempty"`
	Input       string `json:"input,omitempty"`
	Output      string `json:"output,omitempty"`
	ExitCode    int    `json:"exit_code,omitempty"`
	Error       string `json:"error,omitempty"`
	LatencyMS   int64  `json:"latency_ms,omitempty"`
}
