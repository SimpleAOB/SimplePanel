package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestURIEncode(t *testing.T) {
	if got := uriEncode("a b+c/d~", false); got != "a%20b%2Bc/d~" {
		t.Fatal(got)
	}
	if got := canonicalQuery(url.Values{"prefix": {"a/b"}, "list-type": {"2"}, "uploads": {""}}); got != "list-type=2&prefix=a%2Fb&uploads=" {
		t.Fatal(got)
	}
}

// TestSignature checks the signer against the GET Object example in AWS's
// Signature Version 4 documentation.
func TestSignature(t *testing.T) {
	c, err := New(Config{Region: "us-east-1", Bucket: "examplebucket", AccessKey: "AKIAIOSFODNN7EXAMPLE",
		SecretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"})
	if err != nil {
		t.Fatal(err)
	}
	c.base, _ = url.Parse("https://s3.amazonaws.com")
	u := c.objectURL("test.txt")
	req, _ := http.NewRequest("GET", u.String(), nil)
	req.Header.Set("Range", "bytes=0-9")
	tm, _ := time.Parse("20060102T150405Z", "20130524T000000Z")
	c.sign(req, emptySHA, tm)
	want := "AWS4-HMAC-SHA256 Credential=AKIAIOSFODNN7EXAMPLE/20130524/us-east-1/s3/aws4_request, " +
		"SignedHeaders=host;range;x-amz-content-sha256;x-amz-date, Signature=f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// TestLive runs against a real S3-compatible server when SP_TEST_S3 is set
// to "endpoint,bucket,access,secret", e.g. a local MinIO.
func TestLive(t *testing.T) {
	spec := os.Getenv("SP_TEST_S3")
	if spec == "" {
		t.Skip("SP_TEST_S3 not set")
	}
	p := strings.Split(spec, ",")
	c, err := New(Config{Endpoint: p[0], Bucket: p[1], AccessKey: p[2], SecretKey: p[3], PathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := c.Check(ctx, "test/"); err != nil {
		t.Fatal(err)
	}
	// Multipart: a file just over the threshold, with a key that needs escaping.
	data := make([]byte, multipartThreshold+5<<20)
	rand.Read(data)
	path := filepath.Join(t.TempDir(), "big")
	os.WriteFile(path, data, 0o600)
	key := "test/proj/svc/big file+1.bin"
	if n, err := c.UploadFile(ctx, key, path); err != nil || n != int64(len(data)) {
		t.Fatal(n, err)
	}
	objs, err := c.List(ctx, "test/proj/", 100)
	if err != nil || len(objs) != 1 || objs[0].Key != key || objs[0].Size != int64(len(data)) {
		t.Fatalf("%+v %v", objs, err)
	}
	r, _, err := c.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, data) {
		t.Fatal("downloaded data differs")
	}
	if err := c.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if _, _, err := c.Get(ctx, key); err != ErrNotFound {
		t.Fatal("expected not found, got", err)
	}
}
