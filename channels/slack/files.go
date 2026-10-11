package slack

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/slack-go/slack"
)

// File is a file shared with a message (event.files).
type File struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Mimetype           string `json:"mimetype"`
	Size               int64  `json:"size"`
	URLPrivateDownload string `json:"url_private_download,omitempty"`
	// FileAccess is "check_file_info" when the event omits the file's
	// details and files.info must be called.
	FileAccess string `json:"file_access,omitempty"`
}

// FileAPI is the Slack client surface for downloading shared files (scope
// files:read).
type FileAPI interface {
	GetFileInfoContext(ctx context.Context, fileID string, count, page int) (*slack.File, []slack.Comment, *slack.Paging, error)
	GetFileContext(ctx context.Context, downloadURL string, writer io.Writer) error
}

// MaxFileSize bounds each downloaded file.
const MaxFileSize = 50 << 20

// UploadsDir is where shared files land, relative to the workspace.
const UploadsDir = "uploads"

// SavedFile is the outcome for one shared file.
type SavedFile struct {
	Name     string
	Mimetype string
	Size     int64
	// Path is relative to the workspace; empty when the file was not saved.
	Path string
	// Problem explains why the file was not saved.
	Problem string
}

// SaveFiles downloads files into workspace/uploads, never overwriting an
// existing file. A file that cannot be saved is reported, not fatal.
func SaveFiles(ctx context.Context, api FileAPI, files []File, workspace string) []SavedFile {
	dir := filepath.Join(workspace, UploadsDir)
	var out []SavedFile
	for _, f := range files {
		s, err := saveFile(ctx, api, f, dir)
		if err != nil {
			s.Problem = err.Error()
		}
		out = append(out, s)
	}
	return out
}

func saveFile(ctx context.Context, api FileAPI, f File, dir string) (SavedFile, error) {
	if f.URLPrivateDownload == "" || f.FileAccess == "check_file_info" {
		info, _, _, err := api.GetFileInfoContext(ctx, f.ID, 0, 0)
		if err != nil {
			return SavedFile{Name: orDefault(f.Name, f.ID)}, fmt.Errorf("files.info: %w", err)
		}
		f.Name, f.Mimetype, f.Size, f.URLPrivateDownload = info.Name, info.Mimetype, int64(info.Size), info.URLPrivateDownload
	}
	s := SavedFile{Name: orDefault(f.Name, f.ID), Mimetype: f.Mimetype, Size: f.Size}
	if f.URLPrivateDownload == "" {
		return s, errors.New("no download URL (external or deleted file?)")
	}
	if f.Size > MaxFileSize {
		return s, fmt.Errorf("larger than %d MB", MaxFileSize>>20)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return s, err
	}
	tmp, err := os.CreateTemp(dir, ".download-*")
	if err != nil {
		return s, err
	}
	defer os.Remove(tmp.Name()) // no-op once renamed
	lw := &limitWriter{w: tmp, n: MaxFileSize}
	err = api.GetFileContext(ctx, f.URLPrivateDownload, lw)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return s, fmt.Errorf("download: %w", err)
	}
	path, err := freeName(dir, safeName(s.Name))
	if err != nil {
		return s, err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return s, err
	}
	s.Size = lw.written
	s.Path = filepath.Join(UploadsDir, filepath.Base(path))
	return s, nil
}

// AttachmentNote tells the agent which files arrived with the message.
func AttachmentNote(files []SavedFile) string {
	if len(files) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("[Files attached to this message]\n")
	for _, f := range files {
		if f.Path != "" {
			fmt.Fprintf(&b, "- %s (%s, %s), saved in the workspace\n", f.Path, orDefault(f.Mimetype, "unknown type"), humanSize(f.Size))
		} else {
			fmt.Fprintf(&b, "- %s: not saved, %s\n", f.Name, f.Problem)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

var unsafeChars = regexp.MustCompile(`[^\pL\pN._ -]+`)

// safeName keeps a readable file name that cannot leave the uploads dir.
func safeName(name string) string {
	name = unsafeChars.ReplaceAllString(filepath.Base(name), "_")
	name = strings.TrimLeft(strings.TrimSpace(name), ".")
	if name == "" {
		name = "file"
	}
	if len(name) > 120 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:120-len(ext)] + ext
	}
	return name
}

// freeName returns dir/name, or dir/<stem>-2<ext>, ... if taken.
func freeName(dir, name string) (string, error) {
	ext := filepath.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 1; i < 1000; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s-%d%s", stem, i, ext)
		}
		p := filepath.Join(dir, candidate)
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			return p, nil
		}
	}
	return "", fmt.Errorf("no free name for %s", name)
}

type limitWriter struct {
	w       io.Writer
	n       int64
	written int64
}

func (l *limitWriter) Write(p []byte) (int, error) {
	if l.written+int64(len(p)) > l.n {
		return 0, fmt.Errorf("larger than %d MB", l.n>>20)
	}
	n, err := l.w.Write(p)
	l.written += int64(n)
	return n, err
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%d B", n)
	}
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
