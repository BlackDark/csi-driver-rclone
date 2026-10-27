package rclonemount

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/rclone/rclone/fs"
	"github.com/rclone/rclone/fs/config"
	"github.com/unknwon/goconfig" //nolint:misspell // real module path
)

const encryptedHeader = "RCLONE_ENCRYPT_V0:"

var errNeedPassword = errors.New("rclonemount: configData is encrypted but no config password is set")

// LoadConfig puts configData into rclone's in-memory config storage; nothing touches
// disk. Errors never include config contents.
func LoadConfig(configData, password string) error {
	fs.GetConfig(context.Background()).AskPassword = false
	if password != "" {
		if err := config.SetConfigPassword(password); err != nil {
			return fmt.Errorf("rclonemount: config password: %w", err)
		}
	} else if strings.Contains(configData, encryptedHeader) {
		return errNeedPassword
	}
	plain, err := config.Decrypt(strings.NewReader(configData))
	if err != nil {
		return fmt.Errorf("rclonemount: decrypt config: %w", err)
	}
	sections, err := parseINI(plain)
	if err != nil {
		return err
	}
	store := config.LoadedData()
	for name, kv := range sections {
		for k, v := range kv {
			store.SetValue(name, k, v)
		}
	}
	return nil
}

// parseINI uses rclone's own config parser so quoting matches rclone exactly.
func parseINI(r io.Reader) (map[string]map[string]string, error) {
	c, err := goconfig.LoadFromReader(r)
	if err != nil {
		return nil, errors.New("rclonemount: configData is not valid rclone config")
	}
	out := map[string]map[string]string{}
	for _, name := range c.GetSectionList() {
		if name == goconfig.DEFAULT_SECTION {
			continue
		}
		kv, err := c.GetSection(name)
		if err != nil {
			return nil, fmt.Errorf("rclonemount: section %q: %w", name, err)
		}
		out[name] = kv
	}
	return out, nil
}
