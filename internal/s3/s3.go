// Package s3 is a small client for S3-compatible object storage (AWS S3,
// Cloudflare R2, Backblaze B2, Wasabi, MinIO, …). It signs requests with
// AWS Signature Version 4 and covers only what backups need: upload
// (multipart for large files), download, list and delete.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Config locates a bucket.
type Config struct {
	Endpoint  string // e.g. https://<account>.r2.cloudflarestorage.com; empty = AWS
	Region    string // "auto" or e.g. "us-east-1"; empty = us-east-1
	Bucket    string
	AccessKey string
	SecretKey string
	PathStyle bool // https://host/bucket/key instead of https://bucket.host/key
}

type Client struct {
	cfg  Config
	base *url.URL
	http *http.Client
}

// Object is one listed object.
type Object struct {
	Key          string    `json:"key"`
	Size         int64     `json:"size"`
	LastModified time.Time `json:"lastModified"`
}

// Error is a non-2xx answer from the storage service.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("storage: %s: %s (HTTP %d)", e.Code, e.Message, e.Status)
	}
	return fmt.Sprintf("storage: HTTP %d %s", e.Status, e.Message)
}

// ErrNotFound is returned for missing objects.
var ErrNotFound = errors.New("storage: object not found")

// New validates cfg and returns a client.
func New(cfg Config) (*Client, error) {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	ep := strings.TrimRight(strings.TrimSpace(cfg.Endpoint), "/")
	if ep == "" {
		ep = "https://s3." + cfg.Region + ".amazonaws.com"
	}
	if !strings.Contains(ep, "://") {
		ep = "https://" + ep
	}
	u, err := url.Parse(ep)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" {
		return nil, errors.New("endpoint must be a URL like https://s3.example.com")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	if cfg.Bucket == "" {
		return nil, errors.New("bucket is required")
	}
	if cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, errors.New("access key and secret key are required")
	}
	return &Client{cfg: cfg, base: u, http: &http.Client{Timeout: 0}}, nil
}

// objectURL returns the URL of key (or of the bucket when key is "").
func (c *Client) objectURL(key string) *url.URL {
	u := *c.base
	p := u.Path
	if c.cfg.PathStyle {
		p += "/" + c.cfg.Bucket
	} else {
		u.Host = c.cfg.Bucket + "." + u.Host
	}
	if key != "" {
		p += "/" + key
	} else {
		p += "/"
	}
	u.Path = p
	u.RawPath = escapePath(p)
	return &u
}

// escapePath URI-encodes each path segment the way SigV4 expects.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = uriEncode(s, false)
	}
	return strings.Join(segs, "/")
}

func uriEncode(s string, encodeSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_' || ch == '.' || ch == '~' {
			b.WriteByte(ch)
		} else if ch == '/' && !encodeSlash {
			b.WriteByte(ch)
		} else {
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

func canonicalQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vs := append([]string(nil), q[k]...)
		sort.Strings(vs)
		for _, v := range vs {
			parts = append(parts, uriEncode(k, true)+"="+uriEncode(v, true))
		}
	}
	return strings.Join(parts, "&")
}

const emptySHA = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

func hmacSHA(key []byte, s string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(s))
	return h.Sum(nil)
}

// sign adds SigV4 headers to req. payloadHash is the hex SHA-256 of the body.
func (c *Client) sign(req *http.Request, payloadHash string, t time.Time) {
	amzDate := t.UTC().Format("20060102T150405Z")
	day := amzDate[:8]
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)
	host := req.URL.Host
	req.Host = host

	names := []string{"host"}
	vals := map[string]string{"host": host}
	for k := range req.Header {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-amz-") || lk == "content-type" || lk == "content-md5" || lk == "range" {
			names = append(names, lk)
			vals[lk] = strings.TrimSpace(req.Header.Get(k))
		}
	}
	sort.Strings(names)
	var ch strings.Builder
	for _, n := range names {
		ch.WriteString(n + ":" + vals[n] + "\n")
	}
	signed := strings.Join(names, ";")
	canonical := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), canonicalQuery(req.URL.Query()), ch.String(), signed, payloadHash,
	}, "\n")
	scope := day + "/" + c.cfg.Region + "/s3/aws4_request"
	sum := sha256.Sum256([]byte(canonical))
	toSign := "AWS4-HMAC-SHA256\n" + amzDate + "\n" + scope + "\n" + hex.EncodeToString(sum[:])
	k := hmacSHA([]byte("AWS4"+c.cfg.SecretKey), day)
	k = hmacSHA(k, c.cfg.Region)
	k = hmacSHA(k, "s3")
	k = hmacSHA(k, "aws4_request")
	sig := hex.EncodeToString(hmacSHA(k, toSign))
	req.Header.Set("Authorization", fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		c.cfg.AccessKey, scope, signed, sig))
}

// do sends a signed request. body may be nil; payloadHash must match it.
func (c *Client) do(ctx context.Context, method, key string, q url.Values, body io.Reader, size int64, payloadHash string, hdr map[string]string) (*http.Response, error) {
	u := c.objectURL(key)
	if len(q) > 0 {
		u.RawQuery = canonicalQuery(q)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, err
	}
	// Keep the exact escaping we signed.
	req.URL.RawPath = u.RawPath
	req.URL.Path = u.Path
	if body != nil {
		req.ContentLength = size
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	if payloadHash == "" {
		payloadHash = emptySHA
	}
	c.sign(req, payloadHash, time.Now())
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		defer resp.Body.Close()
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var e struct {
			Code    string `xml:"Code"`
			Message string `xml:"Message"`
		}
		_ = xml.Unmarshal(b, &e)
		if resp.StatusCode == http.StatusNotFound && (e.Code == "" || e.Code == "NoSuchKey") {
			return nil, ErrNotFound
		}
		if e.Message == "" {
			e.Message = http.StatusText(resp.StatusCode)
		}
		return nil, &Error{Status: resp.StatusCode, Code: e.Code, Message: e.Message}
	}
	return resp, nil
}

func hashSection(f io.ReaderAt, off, n int64) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, io.NewSectionReader(f, off, n)); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// multipartThreshold is the size above which files are uploaded in parts.
const multipartThreshold = 64 << 20

// UploadFile uploads the file at path as key, using a multipart upload for
// large files so they don't have to fit in a single request.
func (c *Client) UploadFile(ctx context.Context, key, path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	size := fi.Size()
	if size <= multipartThreshold {
		sum, err := hashSection(f, 0, size)
		if err != nil {
			return 0, err
		}
		resp, err := c.do(ctx, http.MethodPut, key, nil, io.NewSectionReader(f, 0, size), size, sum,
			map[string]string{"Content-Type": "application/octet-stream"})
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		return size, nil
	}
	return size, c.multipart(ctx, key, f, size)
}

func (c *Client) multipart(ctx context.Context, key string, f *os.File, size int64) error {
	partSize := int64(32 << 20)
	for size/partSize >= 9000 { // S3 allows 10,000 parts
		partSize *= 2
	}
	resp, err := c.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, nil, 0, "",
		map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	var init struct {
		UploadID string `xml:"UploadId"`
	}
	err = xml.NewDecoder(resp.Body).Decode(&init)
	resp.Body.Close()
	if err != nil || init.UploadID == "" {
		return fmt.Errorf("starting multipart upload: %v", err)
	}
	abort := func() {
		actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if r, err := c.do(actx, http.MethodDelete, key, url.Values{"uploadId": {init.UploadID}}, nil, 0, "", nil); err == nil {
			r.Body.Close()
		}
	}
	type part struct {
		Number int    `xml:"PartNumber"`
		ETag   string `xml:"ETag"`
	}
	var parts []part
	for n, off := 1, int64(0); off < size; n, off = n+1, off+partSize {
		ln := min(partSize, size-off)
		sum, err := hashSection(f, off, ln)
		if err != nil {
			abort()
			return err
		}
		var etag string
		for attempt := 0; attempt < 3; attempt++ {
			var r *http.Response
			r, err = c.do(ctx, http.MethodPut, key, url.Values{"partNumber": {strconv.Itoa(n)}, "uploadId": {init.UploadID}},
				io.NewSectionReader(f, off, ln), ln, sum, nil)
			if err == nil {
				etag = r.Header.Get("ETag")
				r.Body.Close()
				break
			}
			if ctx.Err() != nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
		}
		if err != nil {
			abort()
			return fmt.Errorf("uploading part %d: %w", n, err)
		}
		parts = append(parts, part{Number: n, ETag: etag})
	}
	body, _ := xml.Marshal(struct {
		XMLName xml.Name `xml:"CompleteMultipartUpload"`
		Parts   []part   `xml:"Part"`
	}{Parts: parts})
	sum := sha256.Sum256(body)
	resp, err = c.do(ctx, http.MethodPost, key, url.Values{"uploadId": {init.UploadID}}, bytes.NewReader(body), int64(len(body)),
		hex.EncodeToString(sum[:]), map[string]string{"Content-Type": "application/xml"})
	if err != nil {
		abort()
		return err
	}
	defer resp.Body.Close()
	// S3 can answer 200 with an error document.
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var e struct {
		XMLName xml.Name
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	}
	if xml.Unmarshal(b, &e) == nil && e.XMLName.Local == "Error" {
		abort()
		return &Error{Status: 200, Code: e.Code, Message: e.Message}
	}
	return nil
}

// Put uploads a small in-memory object.
func (c *Client) Put(ctx context.Context, key string, data []byte) error {
	sum := sha256.Sum256(data)
	resp, err := c.do(ctx, http.MethodPut, key, nil, bytes.NewReader(data), int64(len(data)), hex.EncodeToString(sum[:]),
		map[string]string{"Content-Type": "application/octet-stream"})
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// Get downloads key. The caller closes the reader.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, int64, error) {
	resp, err := c.do(ctx, http.MethodGet, key, nil, nil, 0, "", nil)
	if err != nil {
		return nil, 0, err
	}
	return resp.Body, resp.ContentLength, nil
}

// Delete removes key. Missing objects are not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	resp, err := c.do(ctx, http.MethodDelete, key, nil, nil, 0, "", nil)
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// List returns every object whose key starts with prefix (up to max).
func (c *Client) List(ctx context.Context, prefix string, max int) ([]Object, error) {
	var out []Object
	token := ""
	for {
		q := url.Values{"list-type": {"2"}, "prefix": {prefix}, "max-keys": {"1000"}}
		if token != "" {
			q.Set("continuation-token", token)
		}
		resp, err := c.do(ctx, http.MethodGet, "", q, nil, 0, "", nil)
		if err != nil {
			return nil, err
		}
		var page struct {
			Contents []struct {
				Key          string `xml:"Key"`
				Size         int64  `xml:"Size"`
				LastModified string `xml:"LastModified"`
			} `xml:"Contents"`
			IsTruncated           bool   `xml:"IsTruncated"`
			NextContinuationToken string `xml:"NextContinuationToken"`
		}
		err = xml.NewDecoder(io.LimitReader(resp.Body, 32<<20)).Decode(&page)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("reading the bucket listing: %w", err)
		}
		for _, o := range page.Contents {
			t, _ := time.Parse(time.RFC3339, o.LastModified)
			out = append(out, Object{Key: o.Key, Size: o.Size, LastModified: t})
		}
		if !page.IsTruncated || page.NextContinuationToken == "" || len(out) >= max {
			break
		}
		token = page.NextContinuationToken
	}
	if len(out) > max {
		out = out[:max]
	}
	return out, nil
}

// Check verifies the credentials can write, list, read and delete under
// prefix by round-tripping a small test object.
func (c *Client) Check(ctx context.Context, prefix string) error {
	key := strings.TrimLeft(prefix+".simplepanel-check-"+strconv.FormatInt(time.Now().UnixNano(), 36), "/")
	payload := []byte("SimplePanel connection check. Safe to delete.\n")
	if err := c.Put(ctx, key, payload); err != nil {
		return fmt.Errorf("upload failed: %w", err)
	}
	defer c.Delete(context.WithoutCancel(ctx), key)
	r, _, err := c.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	b, _ := io.ReadAll(io.LimitReader(r, 1<<10))
	r.Close()
	if !bytes.Equal(b, payload) {
		return errors.New("download returned different data")
	}
	if _, err := c.List(ctx, key, 1); err != nil {
		return fmt.Errorf("listing failed: %w", err)
	}
	if err := c.Delete(ctx, key); err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}
	return nil
}
