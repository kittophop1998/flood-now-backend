// Package donation validates the optional PromptPay donation configuration.
// The recipient comes only from the environment; if it's missing or invalid
// the feature is simply off.
package donation

import (
	"strings"
	"unicode"
)

// IDType is the PromptPay proxy type, which decides the EMVCo sub-tag the
// client puts the id under.
type IDType string

const (
	IDPhone      IDType = "phone"       // mobile number, 10 digits starting with 0
	IDNationalID IDType = "national_id" // 13-digit national / tax id
	IDEWallet    IDType = "ewallet"     // 15-digit e-wallet id
)

// Config is the public, validated donation setup.
type Config struct {
	PromptPayID   string // digits only, normalized
	IDType        IDType
	RecipientName *string
}

// NormalizePromptPayID strips separators and classifies the id. ok is false
// when it isn't a valid phone number, national/tax id or e-wallet id.
func NormalizePromptPayID(raw string) (id string, t IDType, ok bool) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == ' ' || r == '.':
			// separators people commonly type
		case r == '+' && b.Len() == 0:
			// international prefix, handled below
		default:
			return "", "", false
		}
	}
	d := b.String()
	switch {
	case len(d) == 11 && strings.HasPrefix(d, "66") && d[2] != '0':
		return "0" + d[2:], IDPhone, true
	case len(d) == 10 && d[0] == '0' && d[1] != '0':
		return d, IDPhone, true
	case len(d) == 13 && validThaiIDChecksum(d):
		return d, IDNationalID, true
	case len(d) == 15:
		return d, IDEWallet, true
	}
	return "", "", false
}

// validThaiIDChecksum applies the mod-11 check digit used by Thai national
// and juristic tax ids.
func validThaiIDChecksum(d string) bool {
	sum := 0
	for i := range 12 {
		sum += int(d[i]-'0') * (13 - i)
	}
	return (11-sum%11)%10 == int(d[12]-'0')
}

// Load builds the config from raw env values. It returns nil (feature off)
// unless enabled is an explicit true and the id is valid.
func Load(enabled, promptPayID, recipientName string) *Config {
	switch strings.ToLower(strings.TrimSpace(enabled)) {
	case "true", "1", "yes":
	default:
		return nil
	}
	id, t, ok := NormalizePromptPayID(promptPayID)
	if !ok {
		return nil
	}
	cfg := &Config{PromptPayID: id, IDType: t}
	name := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, strings.TrimSpace(recipientName))
	if name != "" {
		if len([]rune(name)) > 60 {
			name = string([]rune(name)[:60])
		}
		cfg.RecipientName = &name
	}
	return cfg
}
