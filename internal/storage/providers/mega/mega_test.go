// SPDX-License-Identifier: MPL-2.0
package mega

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/luckzk/zy-drive/internal/storage"
	"github.com/luckzk/zy-drive/internal/storage/contracttest"
)

// fakeNode is one node in the fake MEGA tree.
type fakeNode struct {
	hash     string
	name     string
	typ      int
	size     int64
	content  []byte
	children []*fakeNode
	parent   *fakeNode
	mtime    time.Time
}

func (n *fakeNode) Type() int       { return n.typ }
func (n *fakeNode) Name() string    { return n.name }
func (n *fakeNode) Size() int64     { return n.size }
func (n *fakeNode) Time() time.Time { return n.mtime }
func (n *fakeNode) Hash() string    { return n.hash }

// fakeClient is an in-memory megaClient for tests. It mirrors
// go-mega's observable behaviour: PathLookup returns the matched
// prefix plus an error when a component is missing; downloads
// are 1 MiB chunks; Login exports a session that LoginWithKeys
// accepts.
type fakeClient struct {
	mu        sync.Mutex
	root      *fakeNode
	inbox     *fakeNode
	trash     *fakeNode
	nextHash  int
	used      uint64
	total     uint64
	sessionID string
	masterKey []byte
	loggedIn  bool
}

func newFakeClient() *fakeClient {
	c := &fakeClient{used: 1_000_000_000, total: 20_000_000_000}
	c.root = &fakeNode{hash: "root", name: "", typ: nodeRoot, mtime: time.Now()}
	c.inbox = &fakeNode{hash: "inbox", name: "Inbox", typ: nodeInbox, parent: c.root, mtime: time.Now()}
	c.trash = &fakeNode{hash: "trash", name: "Trash", typ: nodeTrash, parent: c.root, mtime: time.Now()}
	c.root.children = []*fakeNode{c.inbox, c.trash}
	return c
}

func (c *fakeClient) newHash() string {
	c.nextHash++
	return fmt.Sprintf("h%d", c.nextHash)
}

func (c *fakeClient) Login(email, password string) error {
	if email == "" || password == "" {
		return fmt.Errorf("mega: email and password required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = "sess-1"
	c.masterKey = []byte("fake-master-key-32-bytes-long!!")
	c.loggedIn = true
	return nil
}

func (c *fakeClient) MultiFactorLogin(email, password, code string) error {
	if code == "" {
		return fmt.Errorf("mega: 2FA code required")
	}
	return c.Login(email, password)
}

func (c *fakeClient) LoginWithKeys(sessionID string, masterKey []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if sessionID != "sess-1" || !bytes.Equal(masterKey, []byte("fake-master-key-32-bytes-long!!")) {
		return fmt.Errorf("mega: invalid session")
	}
	c.sessionID = sessionID
	c.masterKey = masterKey
	c.loggedIn = true
	return nil
}

func (c *fakeClient) Session() (string, []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID, c.masterKey
}

func (c *fakeClient) Quota() (uint64, uint64, error) {
	return c.used, c.total, nil
}

func (c *fakeClient) Root() megaNode { return c.root }

func (c *fakeClient) Children(n megaNode) ([]megaNode, error) {
	f, ok := n.(*fakeNode)
	if !ok {
		return nil, fmt.Errorf("mega: bad node type")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]megaNode, len(f.children))
	for i, ch := range f.children {
		out[i] = ch
	}
	return out, nil
}

func (c *fakeClient) Lookup(root megaNode, names []string) ([]megaNode, error) {
	f, ok := root.(*fakeNode)
	if !ok {
		return nil, fmt.Errorf("mega: bad node type")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []megaNode
	cur := f
	for _, name := range names {
		var found *fakeNode
		for _, ch := range cur.children {
			if ch.name == name {
				found = ch
				break
			}
		}
		if found == nil {
			// go-mega returns the matched prefix plus ENOENT.
			return out, fmt.Errorf("mega: no such node: %q", name)
		}
		out = append(out, found)
		cur = found
	}
	return out, nil
}

func (c *fakeClient) addChild(parent *fakeNode, n *fakeNode) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n.parent = parent
	parent.children = append(parent.children, n)
}

// fakeDownload serves a file's content in 1 MiB chunks.
type fakeDownload struct {
	content []byte
}

func (d *fakeDownload) Chunks() int {
	n := len(d.content) / (1 << 20)
	if len(d.content)%(1<<20) != 0 {
		n++
	}
	return n
}

func (d *fakeDownload) ChunkLocation(id int) (int64, int, error) {
	if id < 0 || id >= d.Chunks() {
		return 0, 0, fmt.Errorf("mega: chunk %d out of range", id)
	}
	pos := int64(id) << 20
	size := 1 << 20
	if rem := len(d.content) - int(pos); rem < size {
		size = rem
	}
	return pos, size, nil
}

func (d *fakeDownload) DownloadChunk(id int) ([]byte, error) {
	pos, size, err := d.ChunkLocation(id)
	if err != nil {
		return nil, err
	}
	return d.content[pos : pos+int64(size)], nil
}

func (d *fakeDownload) Finish() error { return nil }

func (c *fakeClient) Download(n megaNode) (megaDownload, error) {
	f, ok := n.(*fakeNode)
	if !ok || f.typ != nodeFile {
		return nil, fmt.Errorf("mega: not a file")
	}
	return &fakeDownload{content: f.content}, nil
}

// fakeUpload collects chunks and creates the file on Finish.
type fakeUpload struct {
	client *fakeClient
	parent *fakeNode
	name   string
	size   int64
	buf    []byte
}

func (u *fakeUpload) Chunks() int {
	n := u.size / (1 << 20)
	if u.size%(1<<20) != 0 {
		n++
	}
	return int(n)
}

func (u *fakeUpload) ChunkLocation(id int) (int64, int, error) {
	if id < 0 || id >= u.Chunks() {
		return 0, 0, fmt.Errorf("mega: chunk %d out of range", id)
	}
	pos := int64(id) << 20
	size := int64(1 << 20)
	if rem := u.size - pos; rem < size {
		size = rem
	}
	return pos, int(size), nil
}

func (u *fakeUpload) UploadChunk(id int, chunk []byte) error {
	pos, size, err := u.ChunkLocation(id)
	if err != nil {
		return err
	}
	if len(chunk) != size {
		return fmt.Errorf("mega: chunk %d: got %d bytes, want %d", id, len(chunk), size)
	}
	copy(u.buf[pos:pos+int64(size)], chunk)
	return nil
}

func (u *fakeUpload) Finish() (megaNode, error) {
	n := &fakeNode{
		hash: u.client.newHash(), name: u.name, typ: nodeFile,
		size: u.size, content: append([]byte(nil), u.buf...), mtime: time.Now(),
	}
	u.client.addChild(u.parent, n)
	return n, nil
}

func (c *fakeClient) Upload(parent megaNode, name string, size int64) (megaUpload, error) {
	f, ok := parent.(*fakeNode)
	if !ok || (f.typ != nodeFolder && f.typ != nodeRoot) {
		return nil, fmt.Errorf("mega: parent is not a folder")
	}
	return &fakeUpload{client: c, parent: f, name: name, size: size, buf: make([]byte, size)}, nil
}

func (c *fakeClient) CreateDir(parent megaNode, name string) (megaNode, error) {
	f, ok := parent.(*fakeNode)
	if !ok || (f.typ != nodeFolder && f.typ != nodeRoot) {
		return nil, fmt.Errorf("mega: parent is not a folder")
	}
	n := &fakeNode{hash: c.newHash(), name: name, typ: nodeFolder, mtime: time.Now()}
	c.addChild(f, n)
	return n, nil
}

func (c *fakeClient) Rename(n megaNode, name string) error {
	f, ok := n.(*fakeNode)
	if !ok {
		return fmt.Errorf("mega: bad node type")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	f.name = name
	return nil
}

func (c *fakeClient) Move(n megaNode, parent megaNode) error {
	f, ok := n.(*fakeNode)
	if !ok {
		return fmt.Errorf("mega: bad node type")
	}
	p, ok := parent.(*fakeNode)
	if !ok {
		return fmt.Errorf("mega: bad parent type")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if f.parent != nil {
		var kept []*fakeNode
		for _, ch := range f.parent.children {
			if ch != f {
				kept = append(kept, ch)
			}
		}
		f.parent.children = kept
	}
	f.parent = p
	p.children = append(p.children, f)
	return nil
}

func (c *fakeClient) Delete(n megaNode, destroy bool) error {
	f, ok := n.(*fakeNode)
	if !ok {
		return fmt.Errorf("mega: bad node type")
	}
	if !destroy {
		return c.Move(n, c.trash)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if f.parent != nil {
		var kept []*fakeNode
		for _, ch := range f.parent.children {
			if ch != f {
				kept = append(kept, ch)
			}
		}
		f.parent.children = kept
	}
	return nil
}

func (c *fakeClient) ShareLink(n megaNode) (string, error) {
	f, ok := n.(*fakeNode)
	if !ok || f.typ != nodeFile {
		return "", fmt.Errorf("mega: cannot share a folder")
	}
	return "https://mega.nz/file/" + f.hash + "#key", nil
}

// --- tests ---

func TestContract(t *testing.T) {
	contracttest.Run(t, func() (storage.Provider, error) {
		return newProvider(newFakeClient()), nil
	})
}

func TestMegaUploadDownload(t *testing.T) {
	p := newProvider(newFakeClient())
	ctx := context.Background()
	// 2.5 MiB: 3 chunks at the 1 MiB chunk size.
	size := int64(2<<20 + 512<<10)
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	node, err := p.Upload(ctx, "big.bin", bytes.NewReader(content), size)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if node.Size != size {
		t.Fatalf("node.Size: got %d, want %d", node.Size, size)
	}
	rc, err := p.OpenRead(ctx, "big.bin")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	got, err := io.ReadAll(rc)
	_ = rc.Close()
	if err != nil || !bytes.Equal(got, content) {
		t.Fatalf("round-trip mismatch (%d bytes, err %v)", len(got), err)
	}
}

func TestMegaDownloadSeek(t *testing.T) {
	p := newProvider(newFakeClient())
	ctx := context.Background()
	size := int64(3 << 20)
	content := make([]byte, size)
	for i := range content {
		content[i] = byte(i % 251)
	}
	if _, err := p.Upload(ctx, "seek.bin", bytes.NewReader(content), size); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	rc, err := p.OpenRead(ctx, "seek.bin")
	if err != nil {
		t.Fatalf("OpenRead: %v", err)
	}
	defer rc.Close()
	seeker, ok := rc.(io.Seeker)
	if !ok {
		t.Fatalf("OpenRead did not return a seekable reader")
	}
	// Seek into the middle of the second chunk and read across
	// the chunk boundary: the reader must fetch only the chunks
	// it needs.
	off := int64(1<<20 + 100)
	if _, err := seeker.Seek(off, io.SeekStart); err != nil {
		t.Fatalf("Seek: %v", err)
	}
	buf := make([]byte, 1<<20)
	n, err := io.ReadFull(rc, buf)
	if err != nil {
		t.Fatalf("ReadFull after seek: %v", err)
	}
	if !bytes.Equal(buf[:n], content[off:off+int64(n)]) {
		t.Fatalf("seeked read content mismatch")
	}
	// Seek back to the start and read the first bytes.
	if _, err := seeker.Seek(0, io.SeekStart); err != nil {
		t.Fatalf("Seek to start: %v", err)
	}
	head := make([]byte, 10)
	if _, err := io.ReadFull(rc, head); err != nil {
		t.Fatalf("ReadFull head: %v", err)
	}
	if !bytes.Equal(head, content[:10]) {
		t.Fatalf("head content mismatch")
	}
}

func TestMegaSessionExport(t *testing.T) {
	c := newFakeClient()
	if err := c.Login("user@mega.test", "password"); err != nil {
		t.Fatalf("Login: %v", err)
	}
	p := newProvider(c)
	sid, secret, err := p.ExportSession(context.Background())
	if err != nil {
		t.Fatalf("ExportSession: %v", err)
	}
	if sid == "" || secret == "" {
		t.Fatalf("ExportSession returned empty session")
	}
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		t.Fatalf("session secret is not base64: %v", err)
	}
	// A second provider logging in with the exported session
	// must work (this is the restart path).
	c2 := newFakeClient()
	if err := c2.LoginWithKeys(sid, key); err != nil {
		t.Fatalf("LoginWithKeys: %v", err)
	}
	p2 := newProvider(c2)
	nodes, err := p2.List(context.Background(), "")
	if err != nil {
		t.Fatalf("List after session login: %v", err)
	}
	if len(nodes) != 0 {
		t.Fatalf("List: got %d nodes, want 0 (inbox/trash are hidden)", len(nodes))
	}
}

func TestMegaQuota(t *testing.T) {
	p := newProvider(newFakeClient())
	used, total, err := p.Quota(context.Background())
	if err != nil {
		t.Fatalf("Quota: %v", err)
	}
	if used != 1_000_000_000 || total != 20_000_000_000 {
		t.Fatalf("Quota: got (%d, %d), want (1000000000, 20000000000)", used, total)
	}
}

func TestMegaSearch(t *testing.T) {
	p := newProvider(newFakeClient())
	ctx := context.Background()
	if err := p.Mkdir(ctx, "docs"); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	content := []byte("x")
	if _, err := p.Upload(ctx, "docs/report.pdf", bytes.NewReader(content), 1); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if _, err := p.Upload(ctx, "notes.txt", bytes.NewReader(content), 1); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	nodes, err := p.Search(ctx, "report")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(nodes) != 1 || nodes[0].Name != "report.pdf" {
		t.Fatalf("Search: got %v, want [report.pdf]", nodes)
	}
	// The search walks into subfolders.
	nodes, err = p.Search(ctx, "pdf")
	if err != nil {
		t.Fatalf("Search pdf: %v", err)
	}
	if len(nodes) != 1 {
		t.Fatalf("Search pdf: got %d nodes, want 1", len(nodes))
	}
}

func TestMegaShare(t *testing.T) {
	p := newProvider(newFakeClient())
	ctx := context.Background()
	if _, err := p.Upload(ctx, "shared.pdf", bytes.NewReader([]byte("pdf")), 3); err != nil {
		t.Fatalf("Upload: %v", err)
	}
	link, err := p.ShareLink(ctx, "shared.pdf")
	if err != nil {
		t.Fatalf("ShareLink: %v", err)
	}
	if link == "" || !strings.HasSuffix(link, "#key") {
		t.Fatalf("ShareLink: got %q, want a non-empty link", link)
	}
}
