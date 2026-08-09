// Copyright (c) Fianu Labs, Inc. and contributors
// SPDX-License-Identifier: MPL-2.0

package provider

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeCLIConfig(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, cliConfigFile), []byte(body), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return dir
}

func TestReadCLIConfigToken(t *testing.T) {
	future := time.Now().Add(time.Hour).Format(time.RFC3339)
	past := time.Now().Add(-time.Hour).Format(time.RFC3339)

	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "valid non-expired token",
			body: `{"serverDetails":{"accessToken":"tok-abc","tokenExpiresIn":3600,"tokenIssuedAt":"` + future + `"}}`,
			want: "tok-abc",
		},
		{
			name: "no expiry metadata still returns token",
			body: `{"serverDetails":{"accessToken":"tok-noexp"}}`,
			want: "tok-noexp",
		},
		{
			name: "expired token ignored",
			body: `{"serverDetails":{"accessToken":"tok-old","tokenExpiresIn":3600,"tokenIssuedAt":"` + past + `"}}`,
			want: "",
		},
		{
			name: "empty token",
			body: `{"serverDetails":{"accessToken":""}}`,
			want: "",
		},
		{
			name: "unparseable file",
			body: `not json`,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeCLIConfig(t, tt.body)
			t.Setenv(cliHomeDirEnv, dir)
			if got := readCLIConfigToken(); got != tt.want {
				t.Fatalf("readCLIConfigToken() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReadCLIConfigTokenMissingFile(t *testing.T) {
	t.Setenv(cliHomeDirEnv, t.TempDir()) // dir exists, file does not
	if got := readCLIConfigToken(); got != "" {
		t.Fatalf("readCLIConfigToken() = %q, want empty for missing file", got)
	}
}
