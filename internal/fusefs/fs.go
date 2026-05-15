package fusefs

import (
	"context"
	"io"
	"log"
	"os"
	"sync"
	"syscall"
	"time"

	"bazil.org/fuse"
	"bazil.org/fuse/fs"
	"github.com/aydinke/gdrivefs/internal/cache"
	"github.com/aydinke/gdrivefs/internal/drive"
)

var debugLog = log.New(os.Stderr, "[gdrivefs] ", log.LstdFlags)

type Filesystem struct {
	client    *drive.Client
	rootID    string
	inodes    *InodeMap
	openFiles map[uint64]*OpenFile
	mu        sync.RWMutex
	nextFH    uint64
	server    *fs.Server
	nodeCache map[string]fs.Node
	nodeMu    sync.RWMutex
}

type OpenFile struct {
	ID        string
	Name      string
	ParentID  string
	TempPath  string
	Flags     fuse.OpenFlags
	WritePos  int64
	LocalMod  time.Time
	Modified  bool
}

func NewFilesystem(client *drive.Client, rootID string) *Filesystem {
	return &Filesystem{
		client:    client,
		rootID:    rootID,
		inodes:    NewInodeMap(),
		openFiles: make(map[uint64]*OpenFile),
		nextFH:    1,
		nodeCache: make(map[string]fs.Node),
	}
}

func (f *Filesystem) SetServer(server *fs.Server) {
	f.server = server
}

func (f *Filesystem) registerNode(id string, node fs.Node) {
	f.nodeMu.Lock()
	defer f.nodeMu.Unlock()
	f.nodeCache[id] = node
}

func (f *Filesystem) getNode(id string) fs.Node {
	f.nodeMu.RLock()
	defer f.nodeMu.RUnlock()
	return f.nodeCache[id]
}

func (f *Filesystem) invalidateEntry(parentID, name string) {
	if f.server == nil {
		return
	}
	parent := f.getNode(parentID)
	if parent == nil {
		debugLog.Printf("invalidateEntry: parent node not found in cache for %s", parentID)
		return
	}
	debugLog.Printf("invalidateEntry: parentID=%s name=%s", parentID, name)

	if err := f.server.InvalidateEntry(parent, name); err != nil {
		debugLog.Printf("invalidateEntry error: %v", err)
	}

	if err := f.server.InvalidateNodeAttr(parent); err != nil {
		debugLog.Printf("invalidateNodeAttr (parent) error: %v", err)
	}
}

func (f *Filesystem) invalidateNode(id string) {
	if f.server == nil {
		return
	}
	node := f.getNode(id)
	if node == nil {
		debugLog.Printf("invalidateNode: node not found in cache for %s", id)
		return
	}
	debugLog.Printf("invalidateNode: id=%s", id)
	if err := f.server.InvalidateNodeAttr(node); err != nil {
		debugLog.Printf("invalidateNode error: %v", err)
	}
}

func (f *Filesystem) Root() (fs.Node, error) {
	root := &Dir{
		fs:   f,
		ID:   f.rootID,
		Name: "",
	}
	f.registerNode(f.rootID, root)
	return root, nil
}

func (f *Filesystem) Statfs(ctx context.Context, req *fuse.StatfsRequest, resp *fuse.StatfsResponse) error {
	quota, err := f.client.GetQuotaInfo(ctx)
	if err != nil {
		resp.Blocks = 1 << 50
		resp.Bfree = 1 << 50
		resp.Bavail = 1 << 50
		resp.Files = 1 << 20
		resp.Ffree = 1 << 20
		resp.Bsize = 4096
		resp.Namelen = 255
		resp.Frsize = 4096
		return nil
	}

	bsize := uint64(4096)
	resp.Blocks = uint64(quota.Limit) / bsize
	resp.Bfree = uint64(quota.Free) / bsize
	resp.Bavail = uint64(quota.Free) / bsize
	resp.Files = 1 << 20
	resp.Ffree = 1 << 20
	resp.Bsize = uint32(bsize)
	resp.Namelen = 255
	resp.Frsize = uint32(bsize)
	return nil
}

func (f *Filesystem) Destroy() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, of := range f.openFiles {
		if of.TempPath != "" {
			os.Remove(of.TempPath)
		}
	}
	f.openFiles = make(map[uint64]*OpenFile)
}

type InodeMap struct {
	mu     sync.RWMutex
	byID   map[string]uint64
	byIno  map[uint64]string
	next   uint64
}

func NewInodeMap() *InodeMap {
	return &InodeMap{
		byID:  make(map[string]uint64),
		byIno: make(map[uint64]string),
		next:  2,
	}
}

func (m *InodeMap) GetOrAssign(id string) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ino, ok := m.byID[id]; ok {
		return ino
	}
	ino := m.next
	m.next++
	m.byID[id] = ino
	m.byIno[ino] = id
	return ino
}

func (m *InodeMap) GetID(ino uint64) (string, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	id, ok := m.byIno[ino]
	return id, ok
}

type Dir struct {
	fs   *Filesystem
	ID   string
	Name string
}

func (d *Dir) Attr(ctx context.Context, a *fuse.Attr) error {
	a.Mode = os.ModeDir | 0755
	a.Uid = uint32(os.Getuid())
	a.Gid = uint32(os.Getgid())
	a.Inode = d.fs.inodes.GetOrAssign(d.ID)
	a.Valid = 0
	return nil
}

func (d *Dir) Lookup(ctx context.Context, name string) (fs.Node, error) {
	meta, err := d.fs.client.GetFileByName(ctx, d.ID, name)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fuse.ENOENT
		}
		return nil, err
	}
	if meta.IsDir {
		node := &Dir{fs: d.fs, ID: meta.ID, Name: meta.Name}
		d.fs.registerNode(meta.ID, node)
		return node, nil
	}
	node := &File{fs: d.fs, ID: meta.ID, Name: meta.Name, meta: meta}
	d.fs.registerNode(meta.ID, node)
	return node, nil
}

func (d *Dir) ReadDirAll(ctx context.Context) ([]fuse.Dirent, error) {
	children, err := d.fs.client.ListChildren(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	entries := make([]fuse.Dirent, len(children))
	for i, c := range children {
		ino := d.fs.inodes.GetOrAssign(c.ID)
		ent := fuse.Dirent{
			Inode: ino,
			Name:  c.Name,
		}
		if c.IsDir {
			ent.Type = fuse.DT_Dir
		} else {
			ent.Type = fuse.DT_File
		}
		entries[i] = ent
	}
	return entries, nil
}

func (d *Dir) Mkdir(ctx context.Context, req *fuse.MkdirRequest) (fs.Node, error) {
	meta, err := d.fs.client.CreateFolder(ctx, d.ID, req.Name)
	if err != nil {
		return nil, err
	}
	node := &Dir{fs: d.fs, ID: meta.ID, Name: meta.Name}
	d.fs.registerNode(meta.ID, node)
	d.fs.invalidateEntry(d.ID, req.Name)
	return node, nil
}

func (d *Dir) Create(ctx context.Context, req *fuse.CreateRequest, resp *fuse.CreateResponse) (fs.Node, fs.Handle, error) {
	tmpFile, err := d.fs.client.CreateTempUploadFile()
	if err != nil {
		return nil, nil, err
	}
	of := &OpenFile{
		Name:     req.Name,
		ParentID: d.ID,
		TempPath: tmpFile.Name(),
		Flags:    req.Flags,
	}
	d.fs.mu.Lock()
	of.ID = ""
	fh := d.fs.nextFH
	d.fs.nextFH++
	d.fs.openFiles[fh] = of
	d.fs.mu.Unlock()

	node := &File{fs: d.fs, ID: "", Name: req.Name, tempPath: tmpFile.Name(), fh: fh}
	return node, &FileHandle{fs: d.fs, fh: fh}, nil
}

func (d *Dir) Remove(ctx context.Context, req *fuse.RemoveRequest) error {
	meta, err := d.fs.client.GetFileByName(ctx, d.ID, req.Name)
	if err != nil {
		return err
	}
	err = d.fs.client.Delete(ctx, meta.ID)
	if err != nil {
		return err
	}
	d.fs.invalidateEntry(d.ID, req.Name)
	return nil
}

func (d *Dir) Rename(ctx context.Context, req *fuse.RenameRequest, newDir fs.Node) error {
	newDirNode, ok := newDir.(*Dir)
	if !ok {
		return fuse.EIO
	}
	meta, err := d.fs.client.GetFileByName(ctx, d.ID, req.OldName)
	if err != nil {
		return err
	}
	if req.NewName != req.OldName {
		if _, err := d.fs.client.Rename(ctx, meta.ID, req.NewName); err != nil {
			return err
		}
	}
	if newDirNode.ID != d.ID {
		if _, err := d.fs.client.Move(ctx, meta.ID, newDirNode.ID); err != nil {
			return err
		}
	}
	d.fs.invalidateEntry(d.ID, req.OldName)
	d.fs.invalidateEntry(newDirNode.ID, req.NewName)
	return nil
}

type File struct {
	fs       *Filesystem
	ID       string
	Name     string
	meta     *cache.FileMeta
	tempPath string
	fh       uint64
}

func (f *File) Attr(ctx context.Context, a *fuse.Attr) error {
	if f.meta == nil && f.ID != "" {
		meta, err := f.fs.client.GetFile(ctx, f.ID)
		if err != nil {
			return err
		}
		f.meta = meta
	}
	a.Mode = 0644
	a.Uid = uint32(os.Getuid())
	a.Gid = uint32(os.Getgid())
	if f.meta != nil {
		a.Size = uint64(f.meta.Size)
		a.Mtime = f.meta.ModTime
	}
	a.Inode = f.fs.inodes.GetOrAssign(f.ID)
	return nil
}

func (f *File) Open(ctx context.Context, req *fuse.OpenRequest, resp *fuse.OpenResponse) (fs.Handle, error) {
	if f.ID == "" {
		debugLog.Printf("Open: empty ID for file")
		return nil, fuse.EIO
	}

	debugLog.Printf("Open: name=%s id=%s mimeType=%s flags=%v", f.Name, f.ID, f.meta.MimeType, req.Flags)

	tmpFile, err := f.fs.client.CreateTempUploadFile()
	if err != nil {
		debugLog.Printf("Open: create temp file error: %v", err)
		return nil, err
	}

	var rc io.ReadCloser
	if f.meta != nil && drive.IsGoogleDoc(f.meta.MimeType) {
		exportMime := drive.GetExportMimeType(f.meta.MimeType)
		debugLog.Printf("Open: exporting Google Doc as %s", exportMime)
		rc, err = f.fs.client.ExportGoogleDoc(ctx, f.ID, exportMime)
	} else {
		debugLog.Printf("Open: downloading file")
		rc, err = f.fs.client.Download(ctx, f.ID)
	}
	if err != nil {
		tmpFile.Close()
		os.Remove(tmpFile.Name())
		debugLog.Printf("Open: download error for %s: %v", f.Name, err)
		return nil, err
	}

	n, err := tmpFile.ReadFrom(rc)
	rc.Close()
	tmpFile.Close()
	if err != nil {
		os.Remove(tmpFile.Name())
		debugLog.Printf("Open: write temp file error: %v", err)
		return nil, err
	}

	debugLog.Printf("Open: downloaded %d bytes to %s", n, tmpFile.Name())

	of := &OpenFile{
		ID:       f.ID,
		Name:     f.Name,
		ParentID: "",
		TempPath: tmpFile.Name(),
		Flags:    req.Flags,
		LocalMod: time.Now(),
	}

	f.fs.mu.Lock()
	fh := f.fs.nextFH
	f.fs.nextFH++
	f.fs.openFiles[fh] = of
	f.fs.mu.Unlock()

	resp.Flags |= fuse.OpenKeepCache
	debugLog.Printf("Open: assigned fh=%d", fh)
	return &FileHandle{fs: f.fs, fh: fh}, nil
}

type FileHandle struct {
	fs *Filesystem
	fh uint64
}

func (h *FileHandle) Read(ctx context.Context, req *fuse.ReadRequest, resp *fuse.ReadResponse) error {
	h.fs.mu.RLock()
	of, ok := h.fs.openFiles[h.fh]
	h.fs.mu.RUnlock()
	if !ok {
		debugLog.Printf("Read: openFile not found for fh=%d", h.fh)
		return fuse.EIO
	}

	file, err := os.Open(of.TempPath)
	if err != nil {
		debugLog.Printf("Read: open temp file %s error: %v", of.TempPath, err)
		return err
	}
	defer file.Close()

	data := make([]byte, req.Size)
	n, err := file.ReadAt(data, req.Offset)
	if err != nil && err != io.EOF {
		debugLog.Printf("Read: read error at offset=%d size=%d: %v", req.Offset, req.Size, err)
		return err
	}
	resp.Data = data[:n]
	return nil
}

func (h *FileHandle) Write(ctx context.Context, req *fuse.WriteRequest, resp *fuse.WriteResponse) error {
	h.fs.mu.Lock()
	of, ok := h.fs.openFiles[h.fh]
	h.fs.mu.Unlock()
	if !ok {
		return fuse.EIO
	}

	file, err := os.OpenFile(of.TempPath, os.O_WRONLY, 0644)
	if err != nil {
		return err
	}
	defer file.Close()

	n, err := file.WriteAt(req.Data, req.Offset)
	if err != nil {
		return err
	}
	resp.Size = n
	of.Modified = true
	of.WritePos = req.Offset + int64(n)
	return nil
}

func (h *FileHandle) Flush(ctx context.Context, req *fuse.FlushRequest) error {
	h.fs.mu.Lock()
	of, ok := h.fs.openFiles[h.fh]
	h.fs.mu.Unlock()
	if !ok {
		return nil
	}

	if !of.Modified || of.ID == "" {
		return nil
	}

	file, err := os.Open(of.TempPath)
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = h.fs.client.UploadWithConflictCheck(ctx, of.ID, file, of.LocalMod)
	if err != nil {
		if _, ok := err.(*drive.ConflictError); ok {
			return syscall.EBUSY
		}
		return err
	}
	of.Modified = false
	return nil
}

func (h *FileHandle) Release(ctx context.Context, req *fuse.ReleaseRequest) error {
	h.fs.mu.Lock()
	defer h.fs.mu.Unlock()
	of, ok := h.fs.openFiles[h.fh]
	if !ok {
		debugLog.Printf("Release: openFile not found for fh=%d", h.fh)
		return nil
	}

	debugLog.Printf("Release: name=%s parentID=%s modified=%v id=%s", of.Name, of.ParentID, of.Modified, of.ID)

	if of.Modified {
		if of.ID != "" {
			file, err := os.Open(of.TempPath)
			if err == nil {
				debugLog.Printf("Release: updating existing file %s", of.ID)
				h.fs.client.UploadWithConflictCheck(ctx, of.ID, file, of.LocalMod)
				file.Close()
				h.fs.invalidateNode(of.ID)
			}
		} else {
			file, err := os.Open(of.TempPath)
			if err == nil {
				parentID := of.ParentID
				if parentID == "" {
					parentID = h.fs.rootID
				}
				debugLog.Printf("Release: uploading new file %s to parent %s", of.Name, parentID)
				meta, err := h.fs.client.Upload(ctx, parentID, of.Name, file, "application/octet-stream")
				if err == nil {
					of.ID = meta.ID
					debugLog.Printf("Release: upload complete, id=%s", meta.ID)
					h.fs.invalidateEntry(parentID, of.Name)
				} else {
					debugLog.Printf("Release: upload error: %v", err)
				}
				file.Close()
			}
		}
	}

	if of.TempPath != "" {
		os.Remove(of.TempPath)
	}
	delete(h.fs.openFiles, h.fh)
	return nil
}

func (h *FileHandle) Fsync(ctx context.Context, req *fuse.FsyncRequest) error {
	return h.Flush(ctx, nil)
}
