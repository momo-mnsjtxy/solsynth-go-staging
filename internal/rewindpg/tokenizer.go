// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"solsynth.local/go-migration/ports/passport/rewind"
)

var ErrTokenLimit = errors.New("rewind tokenizer input or token limit")

// DictionaryConfig is an operator-selected local UTF-8 dictionary. Each
// non-comment line is 'word positive_frequency [part_of_speech]'; optional stop
// words contain one word per line. No dictionary or remote retrieval is implicit.
type DictionaryConfig struct {
	DictionaryPath, StopWordsPath            string
	DictionarySHA256, StopWordsSHA256        string
	MaxInputBytes, MaxTexts, MaxTokens       int
	MaxDictionaryBytes, MaxDictionaryEntries int
}

type tokenNode struct {
	next      map[rune]*tokenNode
	frequency int64
}

// DictionaryTokenizer is immutable after construction and safe across requests.
// Han runs use dictionary DAG edges and the maximum sum of log(freq/total).
// Unknown Han characters are single-rune edges; other Unicode letters/digits
// form lowercase runs. Punctuation splits runs, and one-rune tokens are omitted.
// Ties sort by count descending then UTF-8 word ascending. This is an owned
// deterministic contract, not a claim of Jieba/HMM or legacy dictionary parity.
type DictionaryTokenizer struct {
	root                         *tokenNode
	total                        float64
	stop                         map[string]bool
	config                       DictionaryConfig
	dictionaryDigest, stopDigest string
}

func dictionaryLimits(c DictionaryConfig) (DictionaryConfig, error) {
	if c.MaxInputBytes == 0 {
		c.MaxInputBytes = 1048576
	}
	if c.MaxTexts == 0 {
		c.MaxTexts = 1000
	}
	if c.MaxTokens == 0 {
		c.MaxTokens = 200000
	}
	if c.MaxDictionaryBytes == 0 {
		c.MaxDictionaryBytes = 16 * 1048576
	}
	if c.MaxDictionaryEntries == 0 {
		c.MaxDictionaryEntries = 200000
	}
	if c.MaxInputBytes < 1 || c.MaxInputBytes > 1048576 || c.MaxTexts < 1 || c.MaxTexts > 1000 || c.MaxTokens < 1 || c.MaxTokens > 200000 || c.MaxDictionaryBytes < 1 || c.MaxDictionaryBytes > 16*1048576 || c.MaxDictionaryEntries < 1 || c.MaxDictionaryEntries > 200000 {
		return c, rewind.ErrInvalid
	}
	if c.StopWordsPath == "" && c.StopWordsSHA256 != "" {
		return c, rewind.ErrInvalid
	}
	return c, nil
}

// readDictionaryFile rejects symlinks and special files before and after open,
// checks bounded size, and reads once during host construction, never under a
// database lock. The frozen in-memory trie is unaffected by later file changes.
func readDictionaryFile(path, expected string, max int) ([]byte, string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, "", rewind.ErrInvalid
	}
	if expected != "" {
		b, e := hex.DecodeString(expected)
		if e != nil || len(b) != sha256.Size || expected != strings.ToLower(expected) {
			return nil, "", rewind.ErrInvalid
		}
	}
	parent, err := os.Open(string(filepath.Separator))
	if err != nil {
		return nil, "", err
	}
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		fd, e := syscall.Openat(int(parent.Fd()), part, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
		parent.Close()
		if e != nil {
			return nil, "", e
		}
		parent = os.NewFile(uintptr(fd), part)
	}
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), parts[len(parts)-1], syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, "", err
	}
	f := os.NewFile(uintptr(fd), path)
	defer f.Close()
	current, err := f.Stat()
	if err != nil {
		return nil, "", err
	}
	if !current.Mode().IsRegular() || current.Size() < 1 || current.Size() > int64(max) {
		return nil, "", rewind.ErrInvalid
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(max)+1))
	if err != nil {
		return nil, "", err
	}
	if len(raw) == 0 || len(raw) > max || !utf8.Valid(raw) || strings.ContainsRune(string(raw), 0) {
		return nil, "", rewind.ErrInvalid
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	if expected != "" && digest != expected {
		return nil, "", errors.New("rewind dictionary digest mismatch")
	}
	return raw, digest, nil
}
func validDictionaryWord(word string) bool {
	if word == "" || len(word) > 256 || utf8.RuneCountInString(word) > 64 {
		return false
	}
	for _, r := range word {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}
func NewDictionaryTokenizer(c DictionaryConfig) (*DictionaryTokenizer, error) {
	c, err := dictionaryLimits(c)
	if err != nil {
		return nil, err
	}
	raw, digest, err := readDictionaryFile(c.DictionaryPath, c.DictionarySHA256, c.MaxDictionaryBytes)
	if err != nil {
		return nil, err
	}
	out := &DictionaryTokenizer{root: &tokenNode{next: map[rune]*tokenNode{}}, stop: map[string]bool{}, config: c, dictionaryDigest: digest}
	words := map[string]bool{}
	var total int64
	nodes := 1
	scan := bufio.NewScanner(strings.NewReader(string(raw)))
	scan.Buffer(make([]byte, 1024), 1024)
	for scan.Scan() {
		line := strings.TrimSpace(scan.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || len(fields) > 3 {
			return nil, rewind.ErrInvalid
		}
		word := strings.ToLower(fields[0])
		frequency, e := strconv.ParseInt(fields[1], 10, 64)
		if e != nil || frequency < 1 || frequency > 1000000000000 || !validDictionaryWord(word) || words[word] || len(words) >= c.MaxDictionaryEntries || total > 1000000000000000000-frequency {
			return nil, rewind.ErrInvalid
		}
		words[word] = true
		total += frequency
		node := out.root
		for _, r := range word {
			if node.next == nil {
				node.next = map[rune]*tokenNode{}
			}
			if node.next[r] == nil {
				nodes++
				if nodes > 1000000 {
					return nil, ErrTokenLimit
				}
				node.next[r] = &tokenNode{}
			}
			node = node.next[r]
		}
		node.frequency = frequency
	}
	if err = scan.Err(); err != nil {
		return nil, err
	}
	if total == 0 {
		return nil, rewind.ErrInvalid
	}
	out.total = math.Log(float64(total))
	if c.StopWordsPath != "" {
		raw, out.stopDigest, err = readDictionaryFile(c.StopWordsPath, c.StopWordsSHA256, c.MaxDictionaryBytes)
		if err != nil {
			return nil, err
		}
		scan = bufio.NewScanner(strings.NewReader(string(raw)))
		scan.Buffer(make([]byte, 1024), 1024)
		for scan.Scan() {
			word := strings.ToLower(strings.TrimSpace(scan.Text()))
			if word == "" || strings.HasPrefix(word, "#") {
				continue
			}
			if !validDictionaryWord(word) || out.stop[word] || len(out.stop) >= c.MaxDictionaryEntries {
				return nil, rewind.ErrInvalid
			}
			out.stop[word] = true
		}
		if err = scan.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}
func (t *DictionaryTokenizer) Ready(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if t == nil || t.root == nil || t.dictionaryDigest == "" {
		return rewind.ErrUnavailable
	}
	return nil
}
func (t *DictionaryTokenizer) TopWords(ctx context.Context, texts []string, limit int) ([]rewind.Word, error) {
	if err := t.Ready(ctx); err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		return nil, rewind.ErrInvalid
	}
	if len(texts) > t.config.MaxTexts {
		return nil, ErrTokenLimit
	}
	size := 0
	for _, v := range texts {
		size += len(v)
		if size > t.config.MaxInputBytes {
			return nil, ErrTokenLimit
		}
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return nil, rewind.ErrInvalid
		}
	}
	counts := map[string]int{}
	tokens := 0
	add := func(word string) error {
		tokens++ // Stop words and omitted single characters still consume the bound.
		if tokens > t.config.MaxTokens {
			return ErrTokenLimit
		}
		if utf8.RuneCountInString(word) > 1 && len(word) <= 256 && !t.stop[word] {
			counts[word]++
		}
		return nil
	}
	for _, text := range texts {
		runes := []rune(strings.ToLower(text))
		for i := 0; i < len(runes); {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if unicode.Is(unicode.Han, runes[i]) {
				end := i + 1
				for end < len(runes) && unicode.Is(unicode.Han, runes[end]) {
					end++
				}
				if err := t.han(ctx, runes[i:end], add); err != nil {
					return nil, err
				}
				i = end
			} else if unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) {
				end := i + 1
				for end < len(runes) && !unicode.Is(unicode.Han, runes[end]) && (unicode.IsLetter(runes[end]) || unicode.IsDigit(runes[end])) {
					end++
				}
				if err := add(string(runes[i:end])); err != nil {
					return nil, err
				}
				i = end
			} else {
				i++
			}
		}
	}
	out := make([]rewind.Word, 0, len(counts))
	for word, n := range counts {
		out = append(out, rewind.Word{Word: word, Count: int64(n)})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Word < out[j].Word
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}
func (t *DictionaryTokenizer) han(ctx context.Context, runes []rune, add func(string) error) error {
	score, next := make([]float64, len(runes)+1), make([]int, len(runes))
	for i := len(runes) - 1; i >= 0; i-- {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		score[i], next[i] = -t.total+score[i+1], i+1
		node := t.root
		for j := i; j < len(runes) && j < i+64; j++ {
			node = node.next[runes[j]]
			if node == nil {
				break
			}
			if node.frequency != 0 {
				candidate := math.Log(float64(node.frequency)) - t.total + score[j+1]
				if candidate > score[i] || candidate == score[i] && j+1 > next[i] {
					score[i], next[i] = candidate, j+1
				}
			}
		}
	}
	for i := 0; i < len(runes); i = next[i] {
		if i%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if err := add(string(runes[i:next[i]])); err != nil {
			return err
		}
	}
	return nil
}
