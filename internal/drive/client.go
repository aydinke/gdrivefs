package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/aydinke/gdrivefs/internal/cache"
	"golang.org/x/oauth2"
	"google.golang.org/api/drive/v3"
	"google.golang.org/api/option"
)

type Client struct {
	svc         *drive.Service
	cache       *cache.Cache
	uploadsDir  string
	httpClient  *http.Client
}

func NewClient(ctx context.Context, token *oauth2.Token, cfg *oauth2.Config, c *cache.Cache, uploadsDir string) (*Client, error) {
	httpClient := cfg.Client(ctx, token)
	svc, err := drive.NewService(ctx, option.WithHTTPClient(httpClient))
	if err != nil {
		return nil, fmt.Errorf("failed to create Drive service: %w", err)
	}
	return &Client{
		svc:        svc,
		cache:      c,
		uploadsDir: uploadsDir,
		httpClient: httpClient,
	}, nil
}

func (c *Client) GetFile(ctx context.Context, id string) (*cache.FileMeta, error) {
	if f, ok := c.cache.Get(id); ok {
		return f, nil
	}
	file, err := c.svc.Files.Get(id).
		Fields("id,name,mimeType,size,modifiedTime,parents,md5Checksum").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	meta := fileToMeta(file)
	c.cache.Put(meta)
	return meta, nil
}

func (c *Client) GetFileByName(ctx context.Context, parentID, name string) (*cache.FileMeta, error) {
	if f, ok := c.cache.GetByName(parentID, name); ok {
		return f, nil
	}
	query := fmt.Sprintf("name='%s' and '%s' in parents and trashed=false", name, parentID)
	files, err := c.svc.Files.List().
		Q(query).
		Fields("files(id,name,mimeType,size,modifiedTime,parents,md5Checksum)").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	if len(files.Files) == 0 {
		return nil, os.ErrNotExist
	}
	meta := fileToMeta(files.Files[0])
	c.cache.Put(meta)
	return meta, nil
}

func (c *Client) ListChildren(ctx context.Context, parentID string) ([]*cache.FileMeta, error) {
	if children, ok := c.cache.ListChildren(parentID); ok {
		return children, nil
	}
	query := fmt.Sprintf("'%s' in parents and trashed=false", parentID)
	var allFiles []*drive.File
	pageToken := ""
	for {
		files, err := c.svc.Files.List().
			Q(query).
			PageToken(pageToken).
			Fields("nextPageToken,files(id,name,mimeType,size,modifiedTime,parents,md5Checksum)").
			PageSize(1000).
			Context(ctx).
			Do()
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, files.Files...)
		if files.NextPageToken == "" {
			break
		}
		pageToken = files.NextPageToken
	}
	metas := make([]*cache.FileMeta, len(allFiles))
	for i, f := range allFiles {
		metas[i] = fileToMeta(f)
	}
	c.cache.PutAll(metas)
	return metas, nil
}

func (c *Client) Download(ctx context.Context, id string) (io.ReadCloser, error) {
	resp, err := c.svc.Files.Get(id).Context(ctx).Download()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func (c *Client) Upload(ctx context.Context, parentID, name string, content io.Reader, mimeType string) (*cache.FileMeta, error) {
	file := &drive.File{
		Name:     name,
		Parents:  []string{parentID},
		MimeType: mimeType,
	}
	uploaded, err := c.svc.Files.Create(file).
		Media(content).
		Fields("id,name,mimeType,size,modifiedTime,parents,md5Checksum").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	meta := fileToMeta(uploaded)
	c.cache.Put(meta)
	c.cache.InvalidateParent(parentID)
	return meta, nil
}

func (c *Client) Update(ctx context.Context, id string, content io.Reader) (*cache.FileMeta, error) {
	uploaded, err := c.svc.Files.Update(id, &drive.File{}).
		Media(content).
		Fields("id,name,mimeType,size,modifiedTime,parents,md5Checksum").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	meta := fileToMeta(uploaded)
	c.cache.Put(meta)
	return meta, nil
}

func (c *Client) CreateFolder(ctx context.Context, parentID, name string) (*cache.FileMeta, error) {
	file := &drive.File{
		Name:     name,
		Parents:  []string{parentID},
		MimeType: "application/vnd.google-apps.folder",
	}
	created, err := c.svc.Files.Create(file).
		Fields("id,name,mimeType,size,modifiedTime,parents,md5Checksum").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	meta := fileToMeta(created)
	c.cache.Put(meta)
	return meta, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	meta, _ := c.cache.Get(id)
	_, err := c.svc.Files.Update(id, &drive.File{Trashed: true}).Context(ctx).Do()
	if err != nil {
		return err
	}
	c.cache.Delete(id)
	if meta != nil && meta.ParentID != "" {
		c.cache.InvalidateParent(meta.ParentID)
	}
	return nil
}

func (c *Client) EmptyTrash(ctx context.Context) error {
	return c.svc.Files.EmptyTrash().Context(ctx).Do()
}

func (c *Client) ListTrash(ctx context.Context) ([]*cache.FileMeta, error) {
	query := "trashed=true"
	var allFiles []*drive.File
	pageToken := ""
	for {
		files, err := c.svc.Files.List().
			Q(query).
			PageToken(pageToken).
			Fields("nextPageToken,files(id,name,mimeType,size,modifiedTime,parents,md5Checksum)").
			PageSize(1000).
			Context(ctx).
			Do()
		if err != nil {
			return nil, err
		}
		allFiles = append(allFiles, files.Files...)
		if files.NextPageToken == "" {
			break
		}
		pageToken = files.NextPageToken
	}
	metas := make([]*cache.FileMeta, len(allFiles))
	for i, f := range allFiles {
		metas[i] = fileToMeta(f)
	}
	return metas, nil
}

func (c *Client) Rename(ctx context.Context, id, newName string) (*cache.FileMeta, error) {
	updated, err := c.svc.Files.Update(id, &drive.File{Name: newName}).
		Fields("id,name,mimeType,size,modifiedTime,parents,md5Checksum").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	meta := fileToMeta(updated)
	c.cache.Put(meta)
	return meta, nil
}

func (c *Client) Move(ctx context.Context, id, newParentID string) (*cache.FileMeta, error) {
	current, err := c.svc.Files.Get(id).Fields("parents").Context(ctx).Do()
	if err != nil {
		return nil, err
	}
	previousParents := ""
	for _, p := range current.Parents {
		if previousParents != "" {
			previousParents += ","
		}
		previousParents += p
	}
	updated, err := c.svc.Files.Update(id, &drive.File{}).
		AddParents(newParentID).
		RemoveParents(previousParents).
		Fields("id,name,mimeType,size,modifiedTime,parents,md5Checksum").
		Context(ctx).
		Do()
	if err != nil {
		return nil, err
	}
	meta := fileToMeta(updated)
	c.cache.Put(meta)
	return meta, nil
}

func (c *Client) GetRootID(ctx context.Context) (string, error) {
	root, err := c.svc.Files.Get("root").Fields("id").Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return root.Id, nil
}

func (c *Client) CreateTempUploadFile() (*os.File, error) {
	if err := os.MkdirAll(c.uploadsDir, 0700); err != nil {
		return nil, err
	}
	return os.CreateTemp(c.uploadsDir, "upload-*")
}

func (c *Client) CheckModifiedSince(ctx context.Context, id string, since time.Time) (bool, error) {
	file, err := c.svc.Files.Get(id).
		Fields("modifiedTime").
		Context(ctx).
		Do()
	if err != nil {
		return false, err
	}
	modTime, err := time.Parse(time.RFC3339, file.ModifiedTime)
	if err != nil {
		return false, err
	}
	return modTime.After(since), nil
}

type ConflictError struct {
	FileID    string
	LocalMod  time.Time
	RemoteMod time.Time
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("conflict: remote file %s was modified after local read", e.FileID)
}

func (c *Client) UploadWithConflictCheck(ctx context.Context, id string, content io.Reader, localMod time.Time) (*cache.FileMeta, error) {
	modified, err := c.CheckModifiedSince(ctx, id, localMod)
	if err != nil {
		return nil, err
	}
	if modified {
		remote, _ := c.GetFile(ctx, id)
		remoteMod, _ := time.Parse(time.RFC3339, remote.ModTime.Format(time.RFC3339))
		return nil, &ConflictError{
			FileID:    id,
			LocalMod:  localMod,
			RemoteMod: remoteMod,
		}
	}
	return c.Update(ctx, id, content)
}

func fileToMeta(f *drive.File) *cache.FileMeta {
	parentID := ""
	if len(f.Parents) > 0 {
		parentID = f.Parents[0]
	}
	modTime, _ := time.Parse(time.RFC3339, f.ModifiedTime)
	return &cache.FileMeta{
		ID:          f.Id,
		Name:        f.Name,
		IsDir:       f.MimeType == "application/vnd.google-apps.folder",
		Size:        f.Size,
		ModTime:     modTime,
		ParentID:    parentID,
		MimeType:    f.MimeType,
		MD5Checksum: f.Md5Checksum,
	}
}

func (c *Client) ExportGoogleDoc(ctx context.Context, id, exportMime string) (io.ReadCloser, error) {
	resp, err := c.svc.Files.Export(id, exportMime).Context(ctx).Download()
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

func IsGoogleDoc(mimeType string) bool {
	googleDocTypes := []string{
		"application/vnd.google-apps.document",
		"application/vnd.google-apps.spreadsheet",
		"application/vnd.google-apps.presentation",
		"application/vnd.google-apps.drawing",
	}
	for _, t := range googleDocTypes {
		if mimeType == t {
			return true
		}
	}
	return false
}

func GetExportMimeType(googleDocType string) string {
	switch googleDocType {
	case "application/vnd.google-apps.document":
		return "application/pdf"
	case "application/vnd.google-apps.spreadsheet":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "application/vnd.google-apps.presentation":
		return "application/pdf"
	case "application/vnd.google-apps.drawing":
		return "image/png"
	default:
		return "application/pdf"
	}
}

func (c *Client) GetFileWithExportMime(ctx context.Context, id string) (*cache.FileMeta, string, error) {
	meta, err := c.GetFile(ctx, id)
	if err != nil {
		return nil, "", err
	}
	if IsGoogleDoc(meta.MimeType) {
		return meta, GetExportMimeType(meta.MimeType), nil
	}
	return meta, "", nil
}

func (c *Client) WatchChanges(ctx context.Context, pageToken string, handler func(*drive.Change)) (string, error) {
	changes, err := c.svc.Changes.List(pageToken).
		Fields("nextPageToken,newStartPageToken,changes(fileId,file(id,name,mimeType,size,modifiedTime,parents,md5Checksum,trashed))").
		Context(ctx).
		Do()
	if err != nil {
		return "", err
	}
	for _, change := range changes.Changes {
		handler(change)
	}
	if changes.NewStartPageToken != "" {
		return changes.NewStartPageToken, nil
	}
	return changes.NextPageToken, nil
}

func (c *Client) GetStartPageToken(ctx context.Context) (string, error) {
	result, err := c.svc.Changes.GetStartPageToken().Context(ctx).Do()
	if err != nil {
		return "", err
	}
	return result.StartPageToken, nil
}

func (c *Client) About(ctx context.Context) (*drive.About, error) {
	return c.svc.About.Get().Fields("user,storageQuota").Context(ctx).Do()
}

type QuotaInfo struct {
	Limit  int64
	Usage  int64
	Free   int64
	User   string
}

func (c *Client) GetQuotaInfo(ctx context.Context) (*QuotaInfo, error) {
	about, err := c.About(ctx)
	if err != nil {
		return nil, err
	}
	var limit, usage int64
	if about.StorageQuota != nil {
		limit = about.StorageQuota.Limit
		usage = about.StorageQuota.Usage
	}
	user := ""
	if about.User != nil {
		user = about.User.EmailAddress
	}
	return &QuotaInfo{
		Limit: limit,
		Usage: usage,
		Free:  limit - usage,
		User:  user,
	}, nil
}

func SavePageToken(path, token string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token), 0600)
}

func LoadPageToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func PrettyJSON(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}
