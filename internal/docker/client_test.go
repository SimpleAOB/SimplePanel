package docker

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestSplitTag(t *testing.T) {
	cases := map[string][2]string{
		"nginx":                   {"nginx", "latest"},
		"nginx:1.27":              {"nginx", "1.27"},
		"localhost:5000/app":      {"localhost:5000/app", "latest"},
		"localhost:5000/app:v2":   {"localhost:5000/app", "v2"},
		"ghcr.io/o/a@sha256:abcd": {"ghcr.io/o/a", "sha256:abcd"},
	}
	for in, want := range cases {
		n, tag := SplitTag(in)
		if n != want[0] || tag != want[1] {
			t.Errorf("SplitTag(%q) = %q, %q", in, n, tag)
		}
	}
}

func TestDemuxAndSort(t *testing.T) {
	var raw bytes.Buffer
	frame := func(stream byte, s string) {
		h := make([]byte, 8)
		h[0] = stream
		binary.BigEndian.PutUint32(h[4:], uint32(len(s)))
		raw.Write(h)
		raw.WriteString(s)
	}
	frame(1, "2026-01-01T00:00:02Z b\n")
	frame(2, "2026-01-01T00:00:01Z a\n")
	var out bytes.Buffer
	if err := demux(&raw, &out); err != nil && err.Error() != "EOF" {
		t.Fatal(err)
	}
	got := sortByTimestamp(out.String())
	if got != "2026-01-01T00:00:01Z a\n2026-01-01T00:00:02Z b\n" {
		t.Errorf("got %q", got)
	}
}

func TestVersionLess(t *testing.T) {
	if !versionLess("1.44", "1.47") || versionLess("1.56", "1.47") || versionLess("1.47", "1.47") {
		t.Error("versionLess wrong")
	}
}
