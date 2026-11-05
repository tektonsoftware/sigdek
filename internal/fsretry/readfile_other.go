//go:build !windows

package fsretry

import "os"

func readFile(path string) ([]byte, error) { return os.ReadFile(path) }
