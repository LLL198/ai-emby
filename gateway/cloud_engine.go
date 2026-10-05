package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func cloudEngineSecret() (string, error) {
	var data [32]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func startCloudEngine() (*exec.Cmd, error) {
	dir := "/app/data/cloud-engine"
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	credentials := filepath.Join(dir, "credentials.json")
	if _, err := os.Stat(credentials); errors.Is(err, os.ErrNotExist) {
		password, err := cloudEngineSecret()
		if err != nil {
			return nil, err
		}
		data, _ := json.Marshal(map[string]string{"username": "admin", "password": password})
		if err := os.WriteFile(credentials, data, 0600); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	var account struct{ Username, Password string }
	data, err := os.ReadFile(credentials)
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(data, &account) != nil || account.Username != "admin" || len(account.Password) != 64 {
		return nil, errors.New("invalid cloud engine credentials")
	}
	secret, err := cloudEngineSecret()
	if err != nil {
		return nil, err
	}
	config, _ := json.Marshal(map[string]any{
		"force": true, "jwt_secret": secret, "token_expires_in": 48,
		"database": map[string]any{"type": "sqlite3", "db_file": filepath.Join(dir, "data.db"), "table_prefix": "x_"},
		"scheme":   map[string]any{"address": "127.0.0.1", "http_port": 18099, "https_port": -1},
		"temp_dir": filepath.Join(dir, "temp"), "bleve_dir": filepath.Join(dir, "bleve"),
		"log": map[string]any{"enable": false}, "max_concurrency": 16,
		"tls_insecure_skip_verify": false, "s3": map[string]any{"enable": false},
		"ftp": map[string]any{"enable": false}, "sftp": map[string]any{"enable": false},
	})
	if err := os.WriteFile(filepath.Join(dir, "config.json"), config, 0600); err != nil {
		return nil, err
	}
	// The engine CLI prints credentials during initialization; keep its output private.
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	setup := exec.CommandContext(ctx, "/usr/local/bin/ai-emby-cloud-engine", "admin", "set", account.Password, "--data", dir)
	setup.Stdout, setup.Stderr = io.Discard, io.Discard
	if err := setup.Run(); err != nil {
		return nil, errors.New("cloud engine initialization failed")
	}
	command := exec.Command("/usr/local/bin/ai-emby-cloud-engine", "server", "--data", dir)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		return nil, errors.New("cloud engine startup failed")
	}
	return command, nil
}
