package ipfsfs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path"
	"strings"

	"tractor.dev/wanix/fs"
	"tractor.dev/wanix/fs/fskit"
)

var _ fs.StatContextFS = (*FS)(nil)

func (fsys *FS) Stat(name string) (fs.FileInfo, error) {
	return fsys.StatContext(context.Background(), name)
}

// StatContext answers stat without fetching file contents. Going through
// Open would cat the whole file just to learn its size, which for a large
// CID means downloading it to answer `ls -l`.
func (fsys *FS) StatContext(ctx context.Context, name string) (fs.FileInfo, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: fs.ErrInvalid}
	}
	if name == "." {
		return fskit.Entry(".", fs.ModeDir|0555), nil
	}
	if name == localDir {
		return fskit.Entry(localDir, fs.ModeDir|0755), nil
	}

	ipfsPath := strings.TrimPrefix(name, localDir+"/")
	st, err := fsys.statPath(ctx, ipfsPath)
	if err != nil {
		return nil, &fs.PathError{Op: "stat", Path: name, Err: err}
	}
	if st.isDir() {
		return fskit.Entry(path.Base(name), fs.ModeDir|0555), nil
	}
	return fskit.Entry(path.Base(name), 0444, st.Size), nil
}

// add uploads data to the node and pins it, returning the CID. Pinning is
// the whole point: unpinned blocks are garbage collected, so content written
// here would silently evaporate.
func (fsys *FS) add(ctx context.Context, name string, data []byte) (string, error) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(data); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	args := url.Values{
		"pin":         {"true"},
		"cid-version": {"1"},
		"quieter":     {"true"},
	}
	u := fmt.Sprintf("%s/api/v0/add?%s", fsys.api, args.Encode())
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := fsys.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ipfs add: %s: %s", resp.Status, truncate(string(respBody), 200))
	}

	// With quieter=true this is a single JSON object, but add can stream one
	// object per line; take the last non-empty line either way.
	lines := strings.Split(strings.TrimSpace(string(respBody)), "\n")
	var out struct{ Hash string }
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &out); err != nil {
		return "", fmt.Errorf("ipfs add: decode: %w", err)
	}
	if out.Hash == "" {
		return "", fmt.Errorf("ipfs add: no CID in response")
	}
	return out.Hash, nil
}

// Unpin removes a recursive pin, letting the blocks be collected.
func (fsys *FS) Unpin(ctx context.Context, cid string) error {
	_, err := fsys.post(ctx, "pin/rm", url.Values{"arg": {cid}})
	return err
}

// Add publishes data under the given name and returns its CID. Exposed so
// callers can publish without going through the file interface.
func (fsys *FS) Add(ctx context.Context, name string, data []byte) (string, error) {
	return fsys.add(ctx, name, data)
}
