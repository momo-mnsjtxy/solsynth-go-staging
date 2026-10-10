// SPDX-License-Identifier: AGPL-3.0-only
package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"solsynth.local/go-migration/ports/authority"
	"solsynth.local/go-migration/ports/federation"
	"strings"
	"syscall"
)

// fileFederationCustody is an explicitly configured local custody boundary.
// Its operation journal and staged keys make the same operation UUID safe to
// replay after a crash between key replacement and database acknowledgement.
type fileFederationCustody struct {
	keyDirectory, operationDirectory string
}

type custodyReceipt struct {
	Operation string `json:"operation"`
	Target    string `json:"target"`
	Action    string `json:"action"`
	Expected  string `json:"expected_fingerprint"`
	Result    string `json:"result_fingerprint"`
}

func (c *fileFederationCustody) lock(ctx context.Context) (*os.File, error) {
	if c == nil {
		return nil, federation.ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(filepath.Join(c.operationDirectory, ".lock"), syscall.O_CREAT|syscall.O_RDWR|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, federation.ErrUnavailable
	}
	lock := os.NewFile(uintptr(fd), "federation-custody-lock")
	lockInfo, err := lock.Stat()
	lockStat, owned := infoSyscallStat(lockInfo)
	if err != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 || !owned || lockStat.Uid != uint32(os.Geteuid()) {
		lock.Close()
		return nil, federation.ErrUnavailable
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		lock.Close()
		return nil, federation.ErrUnavailable
	}
	if err = ctx.Err(); err != nil {
		syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		lock.Close()
		return nil, err
	}
	return lock, nil
}

func unlockCustody(lock *os.File) {
	if lock != nil {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
}

func secureDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("custody directory must be absolute")
	}
	path = filepath.Clean(path)
	resolved, resolveErr := filepath.EvalSymlinks(path)
	info, err := os.Lstat(path)
	stat, owned := infoSyscallStat(info)
	if resolveErr != nil || resolved != path || err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 || !owned || stat.Uid != uint32(os.Geteuid()) {
		return "", errors.New("custody directory must be a private real directory")
	}
	return path, nil
}

func infoSyscallStat(info os.FileInfo) (*syscall.Stat_t, bool) {
	if info == nil {
		return nil, false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	return stat, ok
}

func securePrivateKeyFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("private key must be a private regular file")
	}
	return nil
}

func newFileFederationCustody(keyDirectory, operationDirectory string) (*fileFederationCustody, error) {
	keys, err := secureDirectory(keyDirectory)
	if err != nil {
		return nil, err
	}
	operations, err := secureDirectory(operationDirectory)
	if err != nil || operations == keys {
		return nil, errors.New("custody operation directory must be private and distinct")
	}
	return &fileFederationCustody{keyDirectory: keys, operationDirectory: operations}, nil
}

func readCustodyReceipt(path string) (custodyReceipt, error) {
	var out custodyReceipt
	info, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 8192 {
		return out, errors.New("invalid custody receipt")
	}
	b, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(b, &out) != nil {
		return custodyReceipt{}, errors.New("invalid custody receipt")
	}
	return out, nil
}

func sameCustodyRequest(r custodyReceipt, operation, target, action, expected string) bool {
	return r.Operation == operation && r.Target == target && r.Action == action && r.Expected == expected
}

func syncDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func atomicPrivateFile(path string, body []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".federation-custody-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(body)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(tmpPath, path)
	}
	if err == nil {
		err = syncDirectory(dir)
	}
	return err
}

func keyFingerprintAt(path string) (string, error) {
	if err := securePrivateKeyFile(path); err != nil {
		return "", err
	}
	key, err := issuerKey(path)
	if err != nil {
		return "", err
	}
	return federation.KeyFingerprint(&key.PublicKey)
}

func custodyIsolation(target, keyPath, candidate string, paths map[string]string) error {
	targetInfo, targetErr := os.Lstat(keyPath)
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return federation.ErrUnavailable
	}
	if targetErr == nil {
		if err := securePrivateKeyFile(keyPath); err != nil {
			return federation.ErrUnavailable
		}
		if candidate == "" {
			var err error
			candidate, err = keyFingerprintAt(keyPath)
			if err != nil {
				return federation.ErrUnavailable
			}
		}
	}
	for id, other := range paths {
		if id == target {
			continue
		}
		other = filepath.Clean(other)
		info, err := os.Lstat(other)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || securePrivateKeyFile(other) != nil {
			return federation.ErrUnavailable
		}
		if targetErr == nil {
			a, aok := infoSyscallStat(targetInfo)
			b, bok := infoSyscallStat(info)
			if !aok || !bok {
				return federation.ErrUnavailable
			}
			if a.Dev == b.Dev && a.Ino == b.Ino {
				return federation.ErrForbidden
			}
		}
		fingerprint, err := keyFingerprintAt(other)
		if err != nil {
			return federation.ErrUnavailable
		}
		if candidate != "" && fingerprint == candidate {
			return federation.ErrForbidden
		}
	}
	return nil
}

// LocalKey applies the same cross-actor custody isolation used by mutation
// commands while holding the custody lock through parsing the key. Rotation can
// therefore never swap a path between the isolation scan and the signing read.
func (c *fileFederationCustody) LocalKey(ctx context.Context, target, keyPath string, paths map[string]string) (*rsa.PrivateKey, error) {
	if c == nil || !authority.ValidID(target) || filepath.Dir(filepath.Clean(keyPath)) != c.keyDirectory {
		return nil, federation.ErrInvalid
	}
	lock, err := c.lock(ctx)
	if err != nil {
		return nil, err
	}
	defer unlockCustody(lock)
	if err = custodyIsolation(target, keyPath, "", paths); err != nil {
		return nil, err
	}
	signer, err := issuerKey(keyPath)
	if err != nil {
		return nil, federation.ErrUnavailable
	}
	fingerprint, err := federation.KeyFingerprint(&signer.PublicKey)
	if err != nil {
		return nil, federation.ErrUnavailable
	}
	if err = custodyIsolation(target, keyPath, "", paths); err != nil {
		return nil, err
	}
	current, err := keyFingerprintAt(keyPath)
	if err != nil || current != fingerprint {
		return nil, federation.ErrUnavailable
	}
	return signer, nil
}

func (c *fileFederationCustody) Execute(ctx context.Context, operation, target, keyPath, action, expected string, paths map[string]string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c == nil || !authority.ValidID(operation) || !authority.ValidID(target) || (action != "ensure" && action != "rotate") || filepath.Dir(filepath.Clean(keyPath)) != c.keyDirectory {
		return "", federation.ErrInvalid
	}
	if expected != "" {
		if len(expected) != 64 || strings.ToLower(expected) != expected {
			return "", federation.ErrInvalid
		}
	}
	lock, err := c.lock(ctx)
	if err != nil {
		return "", err
	}
	defer unlockCustody(lock)
	if err = custodyIsolation(target, keyPath, "", paths); err != nil {
		return "", err
	}

	finalPath := filepath.Join(c.operationDirectory, operation+".json")
	pendingPath := filepath.Join(c.operationDirectory, operation+".pending.json")
	stagePath := filepath.Join(c.operationDirectory, operation+".key.pem")
	if receipt, readErr := readCustodyReceipt(finalPath); readErr == nil {
		if !sameCustodyRequest(receipt, operation, target, action, expected) || receipt.Result == "" {
			return "", federation.ErrReplay
		}
		return receipt.Result, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return "", federation.ErrUnavailable
	}

	receipt, pendingErr := readCustodyReceipt(pendingPath)
	if errors.Is(pendingErr, os.ErrNotExist) {
		_ = os.Remove(stagePath) // orphan from a crash before the pending journal
		current, currentErr := keyFingerprintAt(keyPath)
		missing := errors.Is(currentErr, os.ErrNotExist)
		if (currentErr != nil && !missing) || (expected == "" && action == "rotate") || (expected != "" && (missing || current != expected)) || (missing && (action != "ensure" || expected != "")) {
			return "", federation.ErrForbidden
		}
		if action == "ensure" && !missing {
			receipt = custodyReceipt{Operation: operation, Target: target, Action: action, Expected: expected, Result: current}
			journal, _ := json.Marshal(receipt)
			if atomicPrivateFile(finalPath, journal) != nil {
				return "", federation.ErrUnavailable
			}
			return current, nil
		}
		key, generateErr := rsa.GenerateKey(rand.Reader, 3072)
		if generateErr != nil || key.Validate() != nil {
			return "", federation.ErrUnavailable
		}
		body := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
		result, fingerprintErr := federation.KeyFingerprint(&key.PublicKey)
		if fingerprintErr != nil || !missing && result == current {
			return "", federation.ErrUnavailable
		}
		if isolationErr := custodyIsolation(target, keyPath, result, paths); isolationErr != nil {
			return "", isolationErr
		}
		receipt = custodyReceipt{Operation: operation, Target: target, Action: action, Expected: expected, Result: result}
		journal, _ := json.Marshal(receipt)
		if atomicPrivateFile(stagePath, body) != nil || atomicPrivateFile(pendingPath, journal) != nil {
			return "", federation.ErrUnavailable
		}
	} else if pendingErr != nil || !sameCustodyRequest(receipt, operation, target, action, expected) || receipt.Result == "" {
		return "", federation.ErrReplay
	}
	if isolationErr := custodyIsolation(target, keyPath, receipt.Result, paths); isolationErr != nil {
		return "", isolationErr
	}

	current, currentErr := keyFingerprintAt(keyPath)
	missing := errors.Is(currentErr, os.ErrNotExist)
	if currentErr != nil && !missing {
		return "", federation.ErrUnavailable
	}
	if missing || current != receipt.Result {
		if (missing && receipt.Expected != "") || (!missing && current != expected) {
			return "", federation.ErrForbidden
		}
		stageInfo, stageErr := os.Lstat(stagePath)
		if stageErr != nil || !stageInfo.Mode().IsRegular() || stageInfo.Mode().Perm()&0077 != 0 || stageInfo.Size() > 65536 {
			return "", federation.ErrUnavailable
		}
		stage, stageErr := os.ReadFile(stagePath)
		if stageErr != nil {
			return "", federation.ErrUnavailable
		}
		stagedKey, stageErr := issuerKey(stagePath)
		if stageErr != nil {
			return "", federation.ErrUnavailable
		}
		stagedFingerprint, stageErr := federation.KeyFingerprint(&stagedKey.PublicKey)
		if stageErr != nil || stagedFingerprint != receipt.Result || ctx.Err() != nil || atomicPrivateFile(keyPath, stage) != nil {
			return "", federation.ErrUnavailable
		}
	}
	journal, _ := json.Marshal(receipt)
	if atomicPrivateFile(finalPath, journal) != nil {
		return "", federation.ErrUnavailable
	}
	_ = os.Remove(pendingPath)
	_ = os.Remove(stagePath)
	_ = syncDirectory(c.operationDirectory)
	return receipt.Result, nil
}
