package raspapconf

import (
	"encoding/hex"
	"fmt"
	"strings"
)

// DecodeSSID decodes an ssid= value of wpa_supplicant.conf: "literal",
// P"printf-escaped" or hex (wpa_config_parse_string).
func DecodeSSID(raw string) (string, bool) {
	switch {
	case strings.HasPrefix(raw, `P"`):
		end := strings.LastIndex(raw, `"`)
		if end < 2 {
			return "", false
		}
		return DecodeText(raw[2:end]), true
	case strings.HasPrefix(raw, `"`):
		end := strings.LastIndex(raw, `"`)
		if end < 1 {
			return "", false
		}
		return raw[1:end], true
	default:
		decoded, err := hex.DecodeString(raw)
		if err != nil || len(decoded) == 0 {
			return "", false
		}
		return string(decoded), true
	}
}

// EncodeText is wpa_supplicant's printf_encode, the SSID form of
// LIST_NETWORKS and STATUS replies.
func EncodeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\033':
			b.WriteString(`\e`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c >= 32 && c <= 126 {
				b.WriteByte(c)
			} else {
				fmt.Fprintf(&b, `\x%02x`, c)
			}
		}
	}
	return b.String()
}

// DecodeText is wpa_supplicant's printf_decode.
func DecodeText(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i >= len(s) {
			break
		}
		switch c := s[i]; {
		case c == '\\' || c == '"':
			b.WriteByte(c)
		case c == 'n':
			b.WriteByte('\n')
		case c == 'r':
			b.WriteByte('\r')
		case c == 't':
			b.WriteByte('\t')
		case c == 'e':
			b.WriteByte('\033')
		case c == 'x':
			i += decodeHexEscape(&b, s[i+1:])
		case c >= '0' && c <= '7':
			i += decodeOctalEscape(&b, s[i:]) - 1
		}
	}
	return b.String()
}

// decodeHexEscape writes the byte of one or two hex digits and returns how
// many digits it used.
func decodeHexEscape(b *strings.Builder, s string) int {
	if len(s) >= 2 {
		if v, err := hex.DecodeString(s[:2]); err == nil {
			b.WriteByte(v[0])
			return 2
		}
	}
	if len(s) >= 1 {
		if v, ok := hexDigit(s[0]); ok {
			b.WriteByte(v)
			return 1
		}
	}
	return 0
}

func hexDigit(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// decodeOctalEscape writes the byte of up to three octal digits and returns
// how many digits it used.
func decodeOctalEscape(b *strings.Builder, s string) int {
	value, n := 0, 0
	for n < 3 && n < len(s) && s[n] >= '0' && s[n] <= '7' {
		value = value*8 + int(s[n]-'0')
		n++
	}
	b.WriteByte(byte(value))
	return n
}
