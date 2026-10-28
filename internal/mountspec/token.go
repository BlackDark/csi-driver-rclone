package mountspec

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// KeyToken is the rclone config key holding an OAuth token as JSON.
const KeyToken = "token"

// TokenSection is one config section whose OAuth token the driver writes back
// (docs/adr/0004-oauth-token-write-back.md).
type TokenSection struct {
	Section  string `json:"section"`  // name in the user's configData
	Isolated string `json:"isolated"` // name in Remote.ConfigData
	SeedHash string `json:"seedHash"` // hex sha256 of the token in the Credential Secret
}

// HashToken is the seed hash of a token value.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// tokenSections lists the sections of a plaintext config with a non-empty token.
func tokenSections(configData string, rename map[string]string) []TokenSection {
	var out []TokenSection
	eachToken(configData, func(section, value string) string {
		if value != "" {
			out = append(out, TokenSection{Section: section, Isolated: rename[section], SeedHash: HashToken(value)})
		}
		return value
	})
	return out
}

// blankTokens replaces every set token value with a marker, so a refreshed token
// keeps the Identity while a token appearing in an empty section changes it.
func blankTokens(configData string) string {
	return eachToken(configData, func(_, v string) string {
		if v == "" {
			return ""
		}
		return "set"
	})
}

// SetToken replaces the token value of section; other lines stay byte for byte.
func SetToken(configData, section, token string) string {
	return eachToken(configData, func(s, v string) string {
		if s == section {
			return token
		}
		return v
	})
}

// TokenOf returns the token value of section, or "".
func TokenOf(configData, section string) string {
	var out string
	eachToken(configData, func(s, v string) string {
		if s == section {
			out = v
		}
		return v
	})
	return out
}

// eachToken calls f for every "token = value" line and writes back what f returns.
func eachToken(configData string, f func(section, value string) string) string {
	lines := strings.Split(configData, "\n")
	section := ""
	for i, raw := range lines {
		if name, ok := sectionHeader(raw); ok {
			section = name
			continue
		}
		key, value, ok := strings.Cut(raw, "=")
		if !ok || section == "" || strings.TrimSpace(key) != KeyToken {
			continue
		}
		cr := strings.HasSuffix(value, "\r")
		value = strings.TrimSpace(value)
		if next := f(section, value); next != value {
			lines[i] = KeyToken + " = " + next
			if cr {
				lines[i] += "\r"
			}
		}
	}
	return strings.Join(lines, "\n")
}

func parseWriteBack(v string) (bool, error) {
	switch v {
	case "", "true":
		return true, nil
	case "false":
		return false, nil
	}
	return false, newErr(KeyTokenWriteBack, ReasonInvalidValue, "want true or false")
}
