package workflows

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/slack"
	"roamind.ai/channels/web"
)

// ResumeImportName is the resume-import workflow, which writes a resume the
// user uploaded in the web app into their memory, where the job assistant
// and chat turns read it:
//
//	memory → fetch → update
//
// memory sets up the user's Basic Memory project if they have none, as for
// a web-only user; fetch saves the PDF in their workspace's uploads, where
// files they share in Slack go too; update is the chat turn they would have
// in Slack, sharing it ("Remember about me from my resume."), with the
// workspace tools and their memory: the agent updates their own Person note
// by its schema, which says how a resume updates it. The file is the user's
// own, so the turn gets tools, unlike third-party text. Then the upload is
// deleted, unless a newer one has replaced it: memory holds the resume, and
// the workspace a copy. The web app's listener starts it once per upload.
const ResumeImportName = "resume-import"

// resumeFailed is why an import failed, for the user.
const resumeFailed = "Couldn't write your resume into your memory. Upload it again to retry."

// importMessage is what the user would write in Slack, sharing their
// resume.
const importMessage = "Remember about me from my resume."

// ResumeImportInput names the user and the upload, by its Storage
// generation.
type ResumeImportInput struct {
	UserID     string `json:"user_id"`
	UID        string `json:"uid"`
	Generation string `json:"generation"`
}

// importFetch is fetch's result.
type importFetch struct {
	Path string `json:"path"` // relative to the user's workspace
}

// importUpdate is update's result.
type importUpdate struct {
	// Reply is the agent's reply: what it saved.
	Reply   string  `json:"reply"`
	CostUSD float64 `json:"cost_usd,omitempty"`
	// Deleted is whether the upload was deleted; a newer one stays.
	Deleted bool `json:"deleted"`
}

// ResumeImport declares the resume-import workflow. Runs are serialized per
// user. When a task fails for good, the web app is told, and the upload
// stays until the user replaces it.
func ResumeImport(c *hatchet.Client, d Deps) *hatchet.Workflow {
	maxRuns := int32(1)
	strategy := hatchet.GroupRoundRobin
	wf := c.NewWorkflow(ResumeImportName,
		hatchet.WithWorkflowDescription("Write a resume uploaded in the web app into the user's memory"),
		hatchet.WithWorkflowConcurrency(hatchet.Concurrency{
			Expression:    "input.user_id",
			MaxRuns:       &maxRuns,
			LimitStrategy: &strategy,
		}),
	)
	memory := wf.NewTask("memory", func(ctx hatchet.Context, in ResumeImportInput) (memoryProject, error) {
		return d.importMemory(ctx, in)
	},
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
	)
	fetch := wf.NewTask("fetch", func(ctx hatchet.Context, in ResumeImportInput) (importFetch, error) {
		return d.fetchResume(ctx, in)
	},
		hatchet.WithParents(memory),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	wf.NewTask("update", func(ctx hatchet.Context, in ResumeImportInput) (importUpdate, error) {
		var p memoryProject
		var f importFetch
		if err := ctx.ParentOutput(memory, &p); err != nil {
			return importUpdate{}, err
		}
		if err := ctx.ParentOutput(fetch, &f); err != nil {
			return importUpdate{}, err
		}
		return d.updateSelf(ctx, in, p, f)
	},
		hatchet.WithParents(memory, fetch),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(10*time.Minute),
	)
	wf.OnFailure(func(ctx hatchet.Context, in ResumeImportInput) (struct{}, error) {
		return struct{}{}, d.Web.SetResumeImport(ctx, in.UID, web.ResumeImport{Status: "failed", Reason: resumeFailed, Generation: in.Generation, At: d.now()})
	})
	return wf
}

// importMemory tells the web app the import is running and sets up the
// user's memory project if they have none, as for a web-only user.
func (d Deps) importMemory(ctx context.Context, in ResumeImportInput) (memoryProject, error) {
	if err := d.Web.SetResumeImport(ctx, in.UID, web.ResumeImport{Status: "running", Generation: in.Generation, At: d.now()}); err != nil {
		return memoryProject{}, err
	}
	p, err := d.assignProject(ctx, in.UserID)
	if err != nil {
		return p, err
	}
	return d.setUpMemory(p)
}

// fetchResume saves the upload in the user's workspace, as
// uploads/resume-<generation>.pdf, once.
func (d Deps) fetchResume(ctx context.Context, in ResumeImportInput) (importFetch, error) {
	gen, err := strconv.ParseInt(in.Generation, 10, 64)
	if err != nil {
		return importFetch{}, fmt.Errorf("generation %q: %w", in.Generation, err)
	}
	rel := filepath.Join(slack.UploadsDir, "resume-"+in.Generation+".pdf")
	ws, err := agent.WorkspaceDir(d.Agent, in.UserID)
	if err != nil {
		return importFetch{}, err
	}
	path := filepath.Join(ws, rel)
	if _, err := os.Stat(path); err == nil {
		return importFetch{Path: rel}, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return importFetch{}, err
	}
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil {
		return importFetch{}, fmt.Errorf("profile %s: %v", in.UID, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return importFetch{}, err
	}
	// Written whole under a temporary name, so a failed download leaves
	// nothing a retry would take for the file.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".resume-*")
	if err != nil {
		return importFetch{}, err
	}
	defer os.Remove(tmp.Name())
	err = d.Web.DownloadResume(ctx, p.ResumePath, gen, tmp)
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return importFetch{}, err
	}
	return importFetch{Path: rel}, os.Rename(tmp.Name(), path)
}

// updateSelf has the turn the user would have sharing their resume in
// Slack, checks their own Person note is there after it, tells the web app
// and deletes the upload.
func (d Deps) updateSelf(ctx context.Context, in ResumeImportInput, p memoryProject, f importFetch) (importUpdate, error) {
	var out importUpdate
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	ws, err := agent.WorkspaceDir(d.Agent, in.UserID)
	if err != nil {
		return out, err
	}
	info, err := os.Stat(filepath.Join(ws, f.Path))
	if err != nil {
		return out, err
	}
	note := slack.AttachmentNote([]slack.SavedFile{{Name: filepath.Base(f.Path), Mimetype: "application/pdf", Size: info.Size(), Path: f.Path}})
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID: in.UserID, Text: importMessage + "\n\n" + note,
		UserName: u.DisplayName, UserEmail: u.Email, Channel: protocol.ChannelWeb,
		MemoryProject: p.Project, Tools: protocol.ToolsWorkspace,
	}, func(protocol.Packet) {})
	out.Reply, out.CostUSD = strings.TrimSpace(turn.Text), turn.CostUSD
	if err != nil {
		return out, err
	}
	self, err := d.Memory.Self(p.Project)
	if err != nil {
		return out, err
	}
	if self == nil {
		return out, errors.New("the turn left no Person note with relationship self")
	}
	if err := d.Web.SetResumeImport(ctx, in.UID, web.ResumeImport{Status: "done", Generation: in.Generation, At: d.now()}); err != nil {
		return out, err
	}
	slog.Info("resume imported", "user", in.UserID, "note", self.Title)
	out.Deleted = d.deleteUpload(ctx, in)
	return out, nil
}

// deleteUpload deletes the imported upload, unless a newer one has replaced
// it. A failure is only logged: the import is done, and a leftover upload is
// never imported again, since resume_import has its generation.
func (d Deps) deleteUpload(ctx context.Context, in ResumeImportInput) bool {
	p, err := d.Web.Profile(ctx, in.UID)
	if err != nil || p == nil || p.ResumePath == "" {
		slog.Warn("imported resume not deleted: no profile", "uid", in.UID, "err", err)
		return false
	}
	gen, _ := strconv.ParseInt(in.Generation, 10, 64)
	ok, err := d.Web.DeleteResume(ctx, p.ResumePath, gen)
	if err != nil {
		slog.Warn("imported resume not deleted", "uid", in.UID, "err", err)
	}
	return ok
}

// today is the local date, YYYY-MM-DD.
func (d Deps) today() string {
	loc := d.Location
	if loc == nil {
		loc = time.Local
	}
	return d.now().In(loc).Format(time.DateOnly)
}
