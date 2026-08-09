// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// These mirror the Fianu CLI's pkg/config so the provider can pick up a token
// persisted by `fianu auth login` (notably the WIF/federated flow in CI)
// without taking a module dependency on the CLI.
const (
	cliHomeDirEnv = "FIANU_CLI_HOME_DIR" // when set, this IS the config dir
	cliConfigDir  = ".fianu"
	cliConfigFile = "fianu.conf.v1"
)

// cliConfig is the subset of ~/.fianu/fianu.conf.v1 the provider reads.
type cliConfig struct {
	ServerDetails struct {
		AccessToken    string    `json:"accessToken"`
		TokenExpiresIn int       `json:"tokenExpiresIn"`
		TokenIssuedAt  time.Time `json:"tokenIssuedAt"`
	} `json:"serverDetails"`
}

// cliConfigPath mirrors config.getConfFilePath: FIANU_CLI_HOME_DIR is the
// config dir directly when set, otherwise $HOME/.fianu.
func cliConfigPath() (string, error) {
	dir := os.Getenv(cliHomeDirEnv)
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		dir = filepath.Join(home, cliConfigDir)
	}
	return filepath.Join(dir, cliConfigFile), nil
}

// readCLIConfigToken returns a non-expired access token persisted by the CLI,
// or "" when the file is missing, empty, unparseable, or the token has expired.
// It never returns an error: the file is an optional fallback credential source.
func readCLIConfigToken() string {
	path, err := cliConfigPath()
	if err != nil {
		return ""
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg cliConfig
	if err := json.Unmarshal(content, &cfg); err != nil {
		return ""
	}
	d := cfg.ServerDetails
	if d.AccessToken == "" {
		return ""
	}
	if d.TokenExpiresIn > 0 && !d.TokenIssuedAt.IsZero() {
		expiry := d.TokenIssuedAt.Add(time.Duration(d.TokenExpiresIn) * time.Second)
		if time.Now().After(expiry) {
			return ""
		}
	}
	return d.AccessToken
}
