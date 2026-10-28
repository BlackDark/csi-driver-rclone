package mountspec

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/nacl/secretbox"
	"golang.org/x/text/unicode/norm"
)

const encryptedHeader = "RCLONE_ENCRYPT_V0:"

// isEncrypted reports whether the first non-blank, non-comment line is rclone's
// encrypted-config header.
func isEncrypted(configData string) bool {
	sc := bufio.NewScanner(strings.NewReader(configData))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		if l == "" || l[0] == '#' || l[0] == ';' {
			continue
		}
		return l == encryptedHeader
	}
	return false
}

// decryptConfig opens an rclone-encrypted config the way rclone does (secretbox,
// key = sha256("[" + NFKC(password) + "][rclone-config]")), so remote names can
// be isolated per volume.
func decryptConfig(configData, password string) (string, error) {
	if strings.TrimSpace(password) == "" {
		return "", newErr(KeyConfigPassword, ReasonMissing, "configData is encrypted")
	}
	_, body, _ := strings.Cut(configData, encryptedHeader)
	box, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(body), ""))
	if err != nil || len(box) < 24+secretbox.Overhead {
		return "", newErr(KeyConfigData, ReasonInvalidConfig, "malformed encrypted config")
	}
	key := sha256.Sum256([]byte("[" + norm.NFKC.String(password) + "][rclone-config]"))
	var nonce [24]byte
	copy(nonce[:], box[:24])
	plain, ok := secretbox.Open(nil, box[24:], &nonce, &key)
	if !ok {
		return "", newErr(KeyConfigPassword, ReasonInvalidValue, "cannot decrypt configData")
	}
	return string(plain), nil
}
