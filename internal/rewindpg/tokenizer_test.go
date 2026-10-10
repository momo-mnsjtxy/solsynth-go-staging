// SPDX-License-Identifier: AGPL-3.0-only
package rewindpg

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"

	"solsynth.local/go-migration/ports/passport/rewind"
)

func tokenizerFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

const tokenizerVectors = "南京市 100\n南京 1\n长江大桥 10000\n长江 1\n大桥 1\n研究 1000\n研究生 1\n生命 1000\n命 1\n起源 100\n学习 100\n"

func TestDictionaryTokenizerMaximumFrequencyPathUnicodeAndStopWords(t *testing.T) {
	dictionary := tokenizerFile(t, "dictionary.txt", tokenizerVectors)
	stop := tokenizerFile(t, "stop.txt", "# explicit words\n学习\n")
	tokenizer, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: dictionary, StopWordsPath: stop})
	if err != nil {
		t.Fatal(err)
	}
	words, err := tokenizer.TopWords(context.Background(), []string{"南京市长江大桥 研究生命起源 学习。Go42 GO42 école ÉCOLE 😀 未知 X"}, 100)
	want := []rewind.Word{{Word: "go42", Count: 2}, {Word: "école", Count: 2}, {Word: "南京市", Count: 1}, {Word: "生命", Count: 1}, {Word: "研究", Count: 1}, {Word: "起源", Count: 1}, {Word: "长江大桥", Count: 1}}
	if err != nil || !reflect.DeepEqual(words, want) {
		t.Fatal(words, err)
	}
	// Dictionary I/O happens once: requests/readiness use the immutable trie.
	if err = os.Remove(dictionary); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(stop); err != nil {
		t.Fatal(err)
	}
	if err = tokenizer.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	repeat, err := tokenizer.TopWords(context.Background(), []string{"研究生命起源"}, 2)
	if err != nil || !reflect.DeepEqual(repeat, []rewind.Word{{Word: "生命", Count: 1}, {Word: "研究", Count: 1}}) {
		t.Fatal(repeat, err)
	}
}
func TestDictionaryTokenizerBoundedInputTokensAndCancellation(t *testing.T) {
	path := tokenizerFile(t, "dictionary.txt", tokenizerVectors)
	for _, c := range []DictionaryConfig{
		{DictionaryPath: path, MaxInputBytes: 1},
		{DictionaryPath: path, MaxTexts: 1},
		{DictionaryPath: path, MaxTokens: 1},
	} {
		tokenizer, err := NewDictionaryTokenizer(c)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = tokenizer.TopWords(context.Background(), []string{"研究生命", "研究"}, 100); !errors.Is(err, ErrTokenLimit) {
			t.Fatal(c, err)
		}
	}
	tokenizer, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: path})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = tokenizer.TopWords(ctx, []string{strings.Repeat("研究生命", 10000)}, 100); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, v := range []string{string([]byte{0xff}), "word\x00secret"} {
		if _, err = tokenizer.TopWords(context.Background(), []string{v}, 100); err == nil {
			t.Fatal("invalid text accepted")
		}
	}
}
func TestDictionaryTokenizerRejectsMalformedDictionaryAndUnsafeFiles(t *testing.T) {
	for _, body := range []string{"", "词 0\n", "词 -1\n", "词 1000000000001\n", "重复 1\n重复 2\n", "bad-word 1\n", "无频率\n", strings.Repeat("长", 65) + " 1\n", "词 1\x00\n", string([]byte{0xff})} {
		path := tokenizerFile(t, "dictionary.txt", body)
		if _, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: path}); err == nil {
			t.Fatal("malformed dictionary accepted", body)
		}
	}
	path := tokenizerFile(t, "dictionary.txt", tokenizerVectors)
	sum := fmt.Sprintf("%x", sha256.Sum256([]byte(tokenizerVectors)))
	if _, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: path, DictionarySHA256: sum}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []DictionaryConfig{
		{DictionaryPath: path, DictionarySHA256: strings.Repeat("0", 64)},
		{DictionaryPath: path, DictionarySHA256: "bad"},
		{DictionaryPath: path, MaxDictionaryBytes: 1},
		{DictionaryPath: path, MaxDictionaryEntries: 1},
		{DictionaryPath: path, MaxTokens: 200001},
		{DictionaryPath: "relative.txt"},
		{DictionaryPath: filepath.Dir(path)},
	} {
		if _, err := NewDictionaryTokenizer(c); err == nil {
			t.Fatal("invalid dictionary boundary accepted", c)
		}
	}
	link := filepath.Join(t.TempDir(), "link.txt")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: link}); err == nil {
		t.Fatal("symlink accepted")
	}
	directoryLink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(filepath.Dir(path), directoryLink); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: filepath.Join(directoryLink, filepath.Base(path))}); err == nil {
		t.Fatal("ancestor symlink accepted")
	}
	pipe := filepath.Join(t.TempDir(), "pipe")
	if err := syscall.Mkfifo(pipe, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewDictionaryTokenizer(DictionaryConfig{DictionaryPath: pipe}); err == nil {
		t.Fatal("special file accepted")
	}
}
