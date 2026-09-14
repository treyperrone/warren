package aws

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeRecord must not leave a temp file behind, and the dest file has to exist as complete
// JSON. Checking only that no .tmp survives is not enough: a no-op writeRecord would also
// leave a clean directory. A truncate-in-place WriteFile cannot guarantee this under a crash
// or a reader mid-write.
func TestWriteRecordPublishesCompleteJSON(t *testing.T) {
	cache := fakeHome(t)
	const url = "https://one.example.com/start"
	writeRecord(&tokenRecord{StartURL: url, AccessToken: "at", ExpiresAt: stamp(time.Hour)})

	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	var jsonFiles int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q — the rename never happened or cleanup was skipped", e.Name())
		}
		if strings.HasSuffix(e.Name(), ".json") {
			jsonFiles++
		}
	}
	if jsonFiles != 1 {
		t.Fatalf("got %d json files in the cache, want 1 dest file after the rename", jsonFiles)
	}
	got := cachedRecord(url)
	if got == nil || got.AccessToken != "at" {
		t.Errorf("cachedRecord = %+v, want accessToken=at", got)
	}
}

// A crash mid-writeRecord can leave the temp file in the cache dir. cachedRecord must not
// treat it as a token — the in-progress name ends in .tmp, not .json, so it is skipped.
func TestCachedRecordIgnoresWriteRecordTempFile(t *testing.T) {
	cache := fakeHome(t)
	if err := os.WriteFile(filepath.Join(cache, "warren-deadbeef.json.tmp"), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if rec := cachedRecord("https://one.example.com/start"); rec != nil {
		t.Errorf("got %+v from a leftover temp file, want nil", rec)
	}
}

// Two warren processes (or the AWS CLI) can read the cache while another is writing it.
// Truncate-then-write would let a reader observe a half-written file, which cachedRecord
// treats as "nothing cached". After a seed write, concurrent writers and readers must always
// see valid JSON — never nil, never a missing token.
func TestWriteRecordConcurrentReadersSeeValidJSON(t *testing.T) {
	cache := fakeHome(t)
	const url = "https://one.example.com/start"
	writeRecord(&tokenRecord{StartURL: url, AccessToken: "seed", ExpiresAt: stamp(time.Hour)})

	const writers = 8
	const readers = 8
	const iters = 40

	var wg sync.WaitGroup
	errc := make(chan string, readers)

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range iters * writers {
				rec := cachedRecord(url)
				if rec == nil {
					errc <- "cachedRecord returned nil — reader saw a missing or truncated cache file"
					return
				}
				if rec.AccessToken == "" {
					errc <- "empty accessToken — unmarshalled truncated JSON"
					return
				}
			}
		}()
	}

	for w := range writers {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := range iters {
				writeRecord(&tokenRecord{
					StartURL:              url,
					AccessToken:           fmt.Sprintf("tok-%d-%d", id, i),
					ExpiresAt:             stamp(time.Hour),
					RefreshToken:          "rt",
					ClientID:              "cid",
					ClientSecret:          "secret",
					RegistrationExpiresAt: stamp(24 * time.Hour),
				})
			}
		}(w)
	}

	wg.Wait()
	close(errc)
	for msg := range errc {
		t.Error(msg)
	}

	entries, err := os.ReadDir(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file %q", e.Name())
		}
	}
	if rec := cachedRecord(url); rec == nil || rec.AccessToken == "" {
		t.Errorf("after concurrent writes, cachedRecord = %+v, want a live token", rec)
	}
}
