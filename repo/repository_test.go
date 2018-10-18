package repo

import (
	"bytes"
	"compress/gzip"
	"context"
	cryptorand "crypto/rand"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"math/rand"
	"reflect"
	"runtime/debug"
	"testing"
	"time"

	"github.com/kopia/kopia/internal/config"
	"github.com/kopia/kopia/repo/block"
	"github.com/kopia/kopia/repo/internal/storagetesting"
	"github.com/kopia/kopia/repo/object"
	"github.com/kopia/kopia/repo/storage"
)

const masterPassword = "foobarbazfoobarbaz"

func setupTest(t *testing.T, mods ...func(o *NewRepositoryOptions)) (map[string][]byte, map[string]time.Time, *Repository) {
	return setupTestWithData(t, map[string][]byte{}, map[string]time.Time{}, mods...)
}

func setupTestWithData(t *testing.T, data map[string][]byte, keyTime map[string]time.Time, mods ...func(o *NewRepositoryOptions)) (map[string][]byte, map[string]time.Time, *Repository) {
	st := storagetesting.NewMapStorage(data, keyTime, nil)

	opt := &NewRepositoryOptions{
		MaxBlockSize:                400,
		Splitter:                    "FIXED",
		BlockFormat:                 "UNENCRYPTED_HMAC_SHA256",
		MetadataEncryptionAlgorithm: "NONE",
		noHMAC:                      true,
	}

	for _, m := range mods {
		m(opt)
	}

	ctx := context.Background()
	Initialize(ctx, st, opt, masterPassword)

	r, err := connect(ctx, st, &config.LocalConfig{}, masterPassword, &Options{
		//TraceStorage: log.Printf,
	}, block.CachingOptions{})
	if err != nil {
		t.Fatalf("can't connect: %v", err)
	}

	return data, keyTime, r
}

func TestWriters(t *testing.T) {
	cases := []struct {
		data     []byte
		objectID object.ID
	}{
		{
			[]byte("the quick brown fox jumps over the lazy dog"),
			"345acef0bcf82f1daf8e49fab7b7fac7ec296c518501eabea3645b99345a4e08",
		},
		{make([]byte, 100), "1d804f1f69df08f3f59070bf962de69433e3d61ac18522a805a84d8c92741340"}, // 100 zero bytes
	}

	ctx := context.Background()

	for _, c := range cases {
		data, _, repo := setupTest(t)

		writer := repo.Objects.NewWriter(ctx, object.WriterOptions{})

		writer.Write(c.data)

		result, err := writer.Result()
		if err != nil {
			t.Errorf("error getting writer results for %v, expected: %v", c.data, c.objectID.String())
			continue
		}

		repo.Objects.Flush(ctx)

		if !objectIDsEqual(result, c.objectID) {
			t.Errorf("incorrect result for %v, expected: %v got: %v", c.data, c.objectID.String(), result.String())
		}

		repo.Blocks.Flush(ctx)

		if got, want := len(data), 3; got != want {
			// 1 format block + 1 data block + 1 pack index block
			t.Errorf("unexpected data written to the storage (%v), wanted %v", got, want)
			dumpBlockManagerData(data)
		}
	}
}

func dumpBlockManagerData(data map[string][]byte) {
	for k, v := range data {
		if k[0] == 'P' {
			gz, _ := gzip.NewReader(bytes.NewReader(v))
			var buf bytes.Buffer
			buf.ReadFrom(gz)

			var dst bytes.Buffer
			json.Indent(&dst, buf.Bytes(), "", "  ")

			log.Debugf("data[%v] = %v", k, dst.String())
		} else {
			log.Debugf("data[%v] = %x", k, v)
		}
	}
}
func objectIDsEqual(o1 object.ID, o2 object.ID) bool {
	return reflect.DeepEqual(o1, o2)
}

func TestWriterCompleteChunkInTwoWrites(t *testing.T) {
	ctx := context.Background()
	_, _, repo := setupTest(t)

	bytes := make([]byte, 100)
	writer := repo.Objects.NewWriter(ctx, object.WriterOptions{})
	writer.Write(bytes[0:50])
	writer.Write(bytes[0:50])
	result, err := writer.Result()
	if result != "1d804f1f69df08f3f59070bf962de69433e3d61ac18522a805a84d8c92741340" {
		t.Errorf("unexpected result: %v err: %v", result, err)
	}
}

func TestPackingSimple(t *testing.T) {
	data, keyTime, repo := setupTest(t, func(n *NewRepositoryOptions) {
	})
	ctx := context.Background()

	content1 := "hello, how do you do?"
	content2 := "hi, how are you?"
	content3 := "thank you!"

	oid1a := writeObject(ctx, t, repo, []byte(content1), "packed-object-1a")
	oid1b := writeObject(ctx, t, repo, []byte(content1), "packed-object-1b")
	oid2a := writeObject(ctx, t, repo, []byte(content2), "packed-object-2a")
	oid2b := writeObject(ctx, t, repo, []byte(content2), "packed-object-2b")

	oid3a := writeObject(ctx, t, repo, []byte(content3), "packed-object-3a")
	oid3b := writeObject(ctx, t, repo, []byte(content3), "packed-object-3b")
	verify(ctx, t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(ctx, t, repo, oid2a, []byte(content2), "packed-object-2")
	oid2c := writeObject(ctx, t, repo, []byte(content2), "packed-object-2c")
	oid1c := writeObject(ctx, t, repo, []byte(content1), "packed-object-1c")

	repo.Objects.Flush(ctx)
	repo.Blocks.Flush(ctx)

	if got, want := oid1a.String(), oid1b.String(); got != want {
		t.Errorf("oid1a(%q) != oid1b(%q)", got, want)
	}
	if got, want := oid1a.String(), oid1c.String(); got != want {
		t.Errorf("oid1a(%q) != oid1c(%q)", got, want)
	}
	if got, want := oid2a.String(), oid2b.String(); got != want {
		t.Errorf("oid2(%q)a != oidb(%q)", got, want)
	}
	if got, want := oid2a.String(), oid2c.String(); got != want {
		t.Errorf("oid2(%q)a != oidc(%q)", got, want)
	}
	if got, want := oid3a.String(), oid3b.String(); got != want {
		t.Errorf("oid3a(%q) != oid3b(%q)", got, want)
	}

	// format + index + data
	if got, want := len(data), 3; got != want {
		t.Errorf("got unexpected repository contents %v items, wanted %v", got, want)
	}
	repo.Close(ctx)

	data, _, repo = setupTestWithData(t, data, keyTime, func(n *NewRepositoryOptions) {
	})

	verify(ctx, t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(ctx, t, repo, oid2a, []byte(content2), "packed-object-2")
	verify(ctx, t, repo, oid3a, []byte(content3), "packed-object-3")

	if err := repo.Blocks.CompactIndexes(ctx, block.CompactOptions{MinSmallBlocks: 1, MaxSmallBlocks: 1}); err != nil {
		t.Errorf("optimize error: %v", err)
	}
	data, _, repo = setupTestWithData(t, data, keyTime, func(n *NewRepositoryOptions) {
	})

	verify(ctx, t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(ctx, t, repo, oid2a, []byte(content2), "packed-object-2")
	verify(ctx, t, repo, oid3a, []byte(content3), "packed-object-3")

	if err := repo.Blocks.CompactIndexes(ctx, block.CompactOptions{MinSmallBlocks: 1, MaxSmallBlocks: 1}); err != nil {
		t.Errorf("optimize error: %v", err)
	}
	_, _, repo = setupTestWithData(t, data, keyTime, func(n *NewRepositoryOptions) {
	})

	verify(ctx, t, repo, oid1a, []byte(content1), "packed-object-1")
	verify(ctx, t, repo, oid2a, []byte(content2), "packed-object-2")
	verify(ctx, t, repo, oid3a, []byte(content3), "packed-object-3")
}

func TestHMAC(t *testing.T) {
	content := bytes.Repeat([]byte{0xcd}, 50)

	_, _, repo := setupTest(t)

	ctx := context.Background()

	w := repo.Objects.NewWriter(ctx, object.WriterOptions{})
	w.Write(content)
	result, err := w.Result()
	if result.String() != "367352007ee6ca9fa755ce8352347d092c17a24077fd33c62f655574a8cf906d" {
		t.Errorf("unexpected result: %v err: %v", result.String(), err)
	}
}
func TestMalformedStoredData(t *testing.T) {
	data, _, repo := setupTest(t)
	ctx := context.Background()

	cases := [][]byte{
		[]byte("foo\nba"),
		[]byte("foo\nbar1"),
	}

	for _, c := range cases {
		data["a76999788386641a3ec798554f1fe7e6"] = c
		objectID, err := object.ParseID("Da76999788386641a3ec798554f1fe7e6")
		if err != nil {
			t.Errorf("cannot parse object ID: %v", err)
			continue
		}

		reader, err := repo.Objects.Open(ctx, objectID)
		if err == nil || reader != nil {
			t.Errorf("expected error for %x", c)
		}
	}
}

func TestReaderStoredBlockNotFound(t *testing.T) {
	_, _, repo := setupTest(t)
	ctx := context.Background()

	objectID, err := object.ParseID("Ddeadbeef")
	if err != nil {
		t.Errorf("cannot parse object ID: %v", err)
	}
	reader, err := repo.Objects.Open(ctx, objectID)
	if err != storage.ErrBlockNotFound || reader != nil {
		t.Errorf("unexpected result: reader: %v err: %v", reader, err)
	}
}

func TestEndToEndReadAndSeek(t *testing.T) {
	_, _, repo := setupTest(t)
	ctx := context.Background()

	for _, size := range []int{1, 199, 200, 201, 9999, 512434} {
		// Create some random data sample of the specified size.
		randomData := make([]byte, size)
		cryptorand.Read(randomData)

		writer := repo.Objects.NewWriter(ctx, object.WriterOptions{})
		writer.Write(randomData)
		objectID, err := writer.Result()
		writer.Close()
		if err != nil {
			t.Errorf("cannot get writer result for %v: %v", size, err)
			continue
		}

		verify(ctx, t, repo, objectID, randomData, fmt.Sprintf("%v %v", objectID, size))
	}
}

func writeObject(ctx context.Context, t *testing.T, repo *Repository, data []byte, testCaseID string) object.ID {
	w := repo.Objects.NewWriter(ctx, object.WriterOptions{})
	if _, err := w.Write(data); err != nil {
		t.Fatalf("can't write object %q - write failed: %v", testCaseID, err)

	}
	oid, err := w.Result()
	if err != nil {
		t.Fatalf("can't write object %q - result failed: %v", testCaseID, err)
	}

	return oid
}

func verify(ctx context.Context, t *testing.T, repo *Repository, objectID object.ID, expectedData []byte, testCaseID string) {
	t.Helper()
	reader, err := repo.Objects.Open(ctx, objectID)
	if err != nil {
		t.Errorf("cannot get reader for %v (%v): %v %v", testCaseID, objectID, err, string(debug.Stack()))
		return
	}

	for i := 0; i < 20; i++ {
		sampleSize := int(rand.Int31n(300))
		seekOffset := int(rand.Int31n(int32(len(expectedData))))
		if seekOffset+sampleSize > len(expectedData) {
			sampleSize = len(expectedData) - seekOffset
		}
		if sampleSize > 0 {
			got := make([]byte, sampleSize)
			if offset, err := reader.Seek(int64(seekOffset), 0); err != nil || offset != int64(seekOffset) {
				t.Errorf("seek error: %v offset=%v expected:%v", err, offset, seekOffset)
			}
			if n, err := reader.Read(got); err != nil || n != sampleSize {
				t.Errorf("invalid data: n=%v, expected=%v, err:%v", n, sampleSize, err)
			}

			expected := expectedData[seekOffset : seekOffset+sampleSize]

			if !bytes.Equal(expected, got) {
				t.Errorf("incorrect data read for %v: expected: %x, got: %x", testCaseID, expected, got)
			}
		}
	}
}

func TestFormats(t *testing.T) {
	ctx := context.Background()
	makeFormat := func(objectFormat string) func(*NewRepositoryOptions) {
		return func(n *NewRepositoryOptions) {
			n.BlockFormat = objectFormat
			n.ObjectHMACSecret = []byte("key")
			n.MaxBlockSize = 10000
			n.Splitter = "FIXED"
			n.noHMAC = false
		}
	}

	cases := []struct {
		format func(*NewRepositoryOptions)
		oids   map[string]object.ID
	}{
		{
			format: func(n *NewRepositoryOptions) {
				n.MaxBlockSize = 10000
				n.noHMAC = true
			},
			oids: map[string]object.ID{
				"": "b613679a0814d9ec772f95d778c35fc5ff1697c493715653c6c712144292c5ad",
				"The quick brown fox jumps over the lazy dog": "fb011e6154a19b9a4c767373c305275a5a69e8b68b0b4c9200c383dced19a416",
			},
		},
		{
			format: makeFormat("UNENCRYPTED_HMAC_SHA256"),
			oids: map[string]object.ID{
				"The quick brown fox jumps over the lazy dog": "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8",
			},
		},
		{
			format: makeFormat("UNENCRYPTED_HMAC_SHA256_128"),
			oids: map[string]object.ID{
				"The quick brown fox jumps over the lazy dog": "f7bc83f430538424b13298e6aa6fb143",
			},
		},
	}

	for caseIndex, c := range cases {
		_, _, repo := setupTest(t, c.format)

		for k, v := range c.oids {
			bytesToWrite := []byte(k)
			w := repo.Objects.NewWriter(ctx, object.WriterOptions{})
			w.Write(bytesToWrite)
			oid, err := w.Result()
			if err != nil {
				t.Errorf("error: %v", err)
			}
			if !objectIDsEqual(oid, v) {
				t.Errorf("invalid oid for #%v\ngot:\n%#v\nexpected:\n%#v", caseIndex, oid.String(), v.String())
			}

			rc, err := repo.Objects.Open(ctx, oid)
			if err != nil {
				t.Errorf("open failed: %v", err)
				continue
			}
			bytesRead, err := ioutil.ReadAll(rc)
			if err != nil {
				t.Errorf("error reading: %v", err)
			}
			if !bytes.Equal(bytesRead, bytesToWrite) {
				t.Errorf("data mismatch, read:%x vs written:%v", bytesRead, bytesToWrite)
			}
		}
	}
}
