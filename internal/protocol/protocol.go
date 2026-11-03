package protocol

import (
	"agenterm/internal/i18n"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/net/idna"
)

const (
	PathPrefix = "/agenterm"
	ClientPath = PathPrefix + "/client"
	// TokenSecretSize is the number of cryptographically random credential bytes.
	TokenSecretSize = 10
	tokenPrefix     = "Agenterm_"
	tokenIPv4       = 4
	tokenIPv6       = 8
	tokenFormat     = 16 // Namespaced routes; older token formats are unsupported.
	tokenCustomPath = 32 // Length-prefixed URL prefix before the address data.
	tokenHasPort    = 64
	tokenPackedHost = 128
)

const HostnameHeader = "X-Agenterm-Hostname"
const PlatformHeader = "X-Agenterm-Platform"
const ArchHeader = "X-Agenterm-Arch"

// ShellHeader carries the base64url-encoded path of the command shell.
const ShellHeader = "X-Agenterm-Shell"

// DefaultCwdHeader carries the base64url-encoded client working directory.
const DefaultCwdHeader = "X-Agenterm-Default-Cwd"

// SessionControlsHeader advertises supported acknowledged session operations.
const SessionControlsHeader = "X-Agenterm-Session-Controls"
const PersistentSessionsHeader = "X-Agenterm-Persistent-Sessions"

// Version 5 uses session_id alone for client-owned create-or-reuse.
const PersistentSessionsVersion = "5"
const P2PCapabilityHeader = "X-Agenterm-P2P"

// Advertised by servers that send Ping frames and latency measurements.
const HeartbeatHeader = "X-Agenterm-Heartbeat"
const HeartbeatInterval = 20 * time.Second
const HeartbeatTimeout = 60 * time.Second
const WriteTimeout = 10 * time.Second

type Token struct {
	URL        string
	Secret     string
	Identifier string
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
	// Payload: flags, secret, optional path prefix, optional port, and address.
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
	if u.Port() != "" {
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port == 0 {
			return "", i18n.NewError("invalid.token.port.must.be.between.1.and")
		}
		b[0] |= tokenHasPort
		b = binary.BigEndian.AppendUint16(b, uint16(port))
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && ip.Zone() == "" {
		if ip.Is4() {
			b[0] |= tokenIPv4
		} else {
			b[0] |= tokenIPv6
		}
		b = append(b, ip.AsSlice()...)
	} else {
		hostname := u.Hostname()
		if !isASCII(hostname) {
			if ascii, err := idna.Lookup.ToASCII(hostname); err == nil {
				hostname = ascii
			}
		}
		if packed, ok := packHostname(hostname); ok {
			b[0] |= tokenPackedHost
			b = append(b, packed...)
		} else {
			b = append(b, hostname...)
		}
	}
	if t.Identifier != "" {
		if !ValidTokenIdentifier(t.Identifier) {
			return "", i18n.NewError("invalid.token.malformed.host")
		}
		b[0] &^= tokenFormat
		return tokenPrefix + encodeTokenPayload(b) + "_" + t.Identifier, nil
	}
	return tokenPrefix + encodeTokenPayload(b), nil
}
func Decode(s string) (Token, error) {
	var t Token
	payload, ok := strings.CutPrefix(s, tokenPrefix)
	if !ok {
		return t, i18n.NewError("invalid.token.missing.s.prefix", tokenPrefix)
	}
	// A cleared format bit denotes the identifier-bearing format.
	head := strings.IndexByte("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_", payloadByte(payload))
	identifier := ""
	if head >= 0 && byte(head)&4 == 0 {
		// The final underscore separates the fixed-length identifier.
		i := strings.LastIndexByte(payload, '_')
		if i < 0 || !ValidTokenIdentifier(payload[i+1:]) {
			return t, i18n.NewError("invalid.token.malformed.host")
		}
		identifier, payload = payload[i+1:], payload[:i]
	}
	b, err := decodeTokenPayload(payload)
	if err != nil {
		return t, i18n.NewError("invalid.token.w", err)
	}
	if len(b) <= 1+TokenSecretSize {
		return t, i18n.NewError("invalid.token.incomplete.payload")
	}
	if b[0]&tokenFormat == 0 && identifier == "" {
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
	var port uint16
	if b[0]&tokenHasPort != 0 {
		if len(address) < 2 {
			return t, i18n.NewError("invalid.token.malformed.host")
		}
		port = binary.BigEndian.Uint16(address[:2])
		if port == 0 {
			return t, i18n.NewError("invalid.token.malformed.host")
		}
		address = address[2:]
	}
	hostname := string(address)
	switch b[0] &^ (3 | tokenFormat | tokenCustomPath | tokenHasPort) {
	case 0:
	case tokenPackedHost:
		var ok bool
		hostname, ok = unpackHostname(address)
		if !ok {
			return t, i18n.NewError("invalid.token.malformed.host")
		}
	case tokenIPv4, tokenIPv6:
		addressSize := 4
		if b[0]&tokenIPv6 != 0 {
			addressSize = 16
		}
		if len(address) != addressSize {
			return t, i18n.NewError("invalid.token.malformed.ip.address")
		}
		ip, _ := netip.AddrFromSlice(address)
		hostname = ip.String()
	default:
		return t, i18n.NewError("invalid.token.unsupported.address.format")
	}
	host := hostname
	if port != 0 {
		host = net.JoinHostPort(hostname, strconv.Itoa(int(port)))
	} else if strings.Contains(hostname, ":") {
		host = "[" + hostname + "]"
	}
	rawURL := (&url.URL{Scheme: scheme, Host: host, Path: prefix + "/client"}).String()
	u, err := parseTokenURL(rawURL)
	if err != nil {
		return t, err
	}
	if u.Host != host || u.Hostname() != hostname {
		return t, i18n.NewError("invalid.token.malformed.host")
	}
	return Token{URL: rawURL, Secret: base64.RawURLEncoding.EncodeToString(b[1 : 1+TokenSecretSize]), Identifier: identifier}, nil
}

// Packed hosts use six-bit symbols; zero is reserved for padding.
const hostnameAlphabet = "abcdefghijklmnopqrstuvwxyz0123456789-."

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return false
		}
	}
	return true
}

func packHostname(host string) ([]byte, bool) {
	if host == "" {
		return nil, false
	}
	var out []byte
	var bits uint32
	var count uint
	for i := 0; i < len(host); i++ {
		n := strings.IndexByte(hostnameAlphabet, host[i])
		if n < 0 {
			return nil, false
		}
		bits = bits<<6 | uint32(n+1)
		count += 6
		if count >= 8 {
			count -= 8
			out = append(out, byte(bits>>count))
			bits &= (1 << count) - 1
		}
	}
	if count > 0 {
		out = append(out, byte(bits<<(8-count)))
	}
	return out, true
}

func unpackHostname(data []byte) (string, bool) {
	var out strings.Builder
	var bits uint32
	var count uint
	for i, b := range data {
		bits = bits<<8 | uint32(b)
		count += 8
		for count >= 6 {
			count -= 6
			n := (bits >> count) & 63
			if n == 0 && i == len(data)-1 && count == 0 {
				bits = 0
				break
			}
			if n == 0 || n > uint32(len(hostnameAlphabet)) {
				return "", false
			}
			out.WriteByte(hostnameAlphabet[n-1])
			bits &= (1 << count) - 1
		}
	}
	// Unused padding bits must be zero.
	if count >= 6 || bits != 0 || out.Len() == 0 {
		return "", false
	}
	return out.String(), true
}

// Escape the two non-alphanumeric Base64URL symbols using underscores so
// the complete token stays selectable as a single word in common terminals.
func encodeTokenPayload(data []byte) string {
	encoded := base64.RawURLEncoding.EncodeToString(data)
	return strings.NewReplacer("_", "_0", "-", "_1").Replace(encoded)
}

func decodeTokenPayload(encoded string) ([]byte, error) {
	var decoded strings.Builder
	decoded.Grow(len(encoded))
	for i := 0; i < len(encoded); i++ {
		c := encoded[i]
		if c == '_' {
			i++
			if i >= len(encoded) {
				return nil, fmt.Errorf("incomplete token escape")
			}
			switch encoded[i] {
			case '0':
				decoded.WriteByte('_')
			case '1':
				decoded.WriteByte('-')
			default:
				return nil, fmt.Errorf("invalid token escape")
			}
		} else if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			decoded.WriteByte(c)
		} else {
			return nil, fmt.Errorf("invalid token character")
		}
	}
	return base64.RawURLEncoding.Strict().DecodeString(decoded.String())
}

func payloadByte(payload string) byte {
	if payload == "" {
		return 0
	}
	return payload[0]
}

// ValidTokenIdentifier validates a public identifier, never an authentication secret.
func ValidTokenIdentifier(id string) bool {
	if len(id) != 6 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// ConnectionURL includes the public identifier in the WebSocket route.
func (t Token) ConnectionURL() string {
	if t.Identifier == "" {
		return t.URL
	}
	return t.URL + "/" + t.Identifier
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
	Type      string `json:"type"`
	ID        string `json:"id,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Command   string `json:"command,omitempty"`
	ExecOptions
	SessionCreate     bool       `json:"session_create,omitempty"`
	SessionID         string     `json:"session_id,omitempty"`
	WaitMS            int        `json:"wait_ms"`
	SessionPersistent bool       `json:"session_persistent,omitempty"`
	SessionPromoted   bool       `json:"session_promoted,omitempty"`
	Interactive       bool       `json:"interactive,omitempty"`
	Input             string     `json:"input,omitempty"`
	Output            string     `json:"output,omitempty"`
	Stream            string     `json:"stream,omitempty"`
	ExitCode          int        `json:"exit_code,omitempty"`
	TimedOut          bool       `json:"timed_out,omitempty"`
	Canceled          bool       `json:"canceled,omitempty"`
	Error             string     `json:"error,omitempty"`
	LatencyMS         int64      `json:"latency_ms,omitempty"`
	P2P               *P2PLaunch `json:"p2p,omitempty"`
}

// P2PLaunch runs a built-in bootstrap without putting its short-lived credential
// in the displayed command. File payloads are never carried in this protocol.
type P2PLaunch struct {
	Script       string `json:"script"`
	StopScript   string `json:"stop_script"`
	ConfigBase64 string `json:"config_base64"`
}
