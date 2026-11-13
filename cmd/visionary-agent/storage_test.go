package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageWalk(t *testing.T) {
	root := t.TempDir()
	write := func(rel string, n int) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, make([]byte, n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("var/log/big.log", 300_000)
	write("var/log/deep/a/b/c.log", 100_000)
	write("var/cache/x", 50_000)
	write("home/u/file", 20_000)
	write("swapfile", 400_000)

	w := &storageWalk{ctx: context.Background(), links: map[[2]uint64]struct{}{}}
	items := w.mount(root)
	got := map[string]int64{}
	for _, it := range items {
		if it.Kind != "dir" || it.Mount != root {
			t.Fatalf("item %+v", it)
		}
		got[strings.TrimPrefix(filepath.ToSlash(strings.TrimPrefix(it.Path, root)), "/")] = it.Bytes
	}
	// Allocated sizes vary by filesystem; the order and the nesting must hold.
	if got["var"] < got["var/log"] || got["var/log"] < 400_000 || got["home/u"] == 0 || got["swapfile"] < 400_000 {
		t.Fatalf("sizes %v", got)
	}
	if got["var"] != got["var/log"]+got["var/cache"] {
		t.Fatalf("var is not the sum of its parts: %v", got)
	}
	if w.partial {
		t.Fatal("small walk marked partial")
	}

	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	w = &storageWalk{ctx: ctx, links: map[[2]uint64]struct{}{}}
	w.mount(root)
	if !w.partial {
		t.Fatal("a walk past its time is not marked partial")
	}
}

func TestEngineStorageItems(t *testing.T) {
	var df dockerDF
	body := `{"LayersSize": 9000,
	  "Images": [{"Id": "sha256:aaaaaaaaaaaaaaaa", "RepoTags": ["<none>:<none>"], "Size": 100}, {"Id": "sha256:b", "RepoTags": ["api:1.2"], "Size": 800}],
	  "Containers": [{"Names": ["/web"], "SizeRw": 42}, {"Names": ["/idle"], "SizeRw": 0}],
	  "Volumes": [{"Name": "pgdata", "UsageData": {"Size": 5000}}, {"Name": "unsized", "UsageData": {"Size": -1}}, {"Name": "nil"}],
	  "BuildCache": [{"Size": 70, "Shared": false}, {"Size": 30, "Shared": true}]}`
	if err := json.Unmarshal([]byte(body), &df); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range dfItems(df) {
		got = append(got, it.Kind+" "+it.Path)
	}
	want := "docker_images images|docker_image api:1.2|docker_image aaaaaaaaaaaa|docker_container web|docker_volume pgdata|docker_build_cache build cache"
	if strings.Join(got, "|") != want {
		t.Fatalf("got %s", strings.Join(got, "|"))
	}
}

func TestParseScanEvery(t *testing.T) {
	for in, want := range map[string]time.Duration{"": 6 * time.Hour, "off": 0, "12h": 12 * time.Hour} {
		if d, err := parseScanEvery(in); err != nil || d != want {
			t.Errorf("%q: %v %v", in, d, err)
		}
	}
	if _, err := parseScanEvery("1m"); err == nil {
		t.Error("1m accepted")
	}
}
