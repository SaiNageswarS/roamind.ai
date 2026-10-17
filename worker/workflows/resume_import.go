package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"

	"roamind.ai/agent"
	"roamind.ai/agent/protocol"
	"roamind.ai/channels/web"
	"roamind.ai/memory/store/notes"
)

// ResumeImportName is the resume-import workflow, which writes a resume the
// user uploaded in the web app into their memory, where the job assistant
// and chat turns read it:
//
//	memory → read → extract → write
//
// memory sets up the user's Basic Memory project if they have none, as for
// a web-only user; read reads the PDF as text; extract has the agent list
// its facts; write merges them into the user's own Person note. A PDF with
// no text, such as a scan, is reported to the web app by read, and the
// tasks after it are skipped. The web app's listener starts it once per
// upload.
const ResumeImportName = "resume-import"

// maxResume is how much of a resume's text is kept.
const maxResume = 30000

// Reasons an import failed, for the user.
const (
	resumeScan   = "Couldn't read text from the PDF; it may be a scan. Upload one with selectable text."
	resumeFailed = "Couldn't write your resume into your memory. Upload it again to retry."
)

// ResumeImportInput names the user and the upload, by its Storage
// generation.
type ResumeImportInput struct {
	UserID     string `json:"user_id"`
	UID        string `json:"uid"`
	Generation string `json:"generation"`
}

// importRead is read's result; Skip, always present for the later tasks'
// skip condition, says why the resume can't be imported.
type importRead struct {
	Text string `json:"text"`
	Skip string `json:"skip"`
}

// importExtract is extract's result.
type importExtract struct {
	Resume  agent.ExtractedResume `json:"resume"`
	CostUSD float64               `json:"cost_usd,omitempty"`
}

// importWrite is write's result.
type importWrite struct {
	Note string `json:"note"` // the Person note's title
}

// errScan is readResume's error for a PDF without a text layer.
var errScan = errors.New("the resume has almost no text; it may be a scan")

// ResumeImport declares the resume-import workflow. Runs are serialized per
// user. When a task fails for good, the web app is told why.
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
	read := wf.NewTask("read", func(ctx hatchet.Context, in ResumeImportInput) (importRead, error) {
		gen, err := strconv.ParseInt(in.Generation, 10, 64)
		if err != nil {
			return importRead{}, fmt.Errorf("generation %q: %w", in.Generation, err)
		}
		p, err := d.Web.Profile(ctx, in.UID)
		if err != nil || p == nil {
			return importRead{}, fmt.Errorf("profile %s: %v", in.UID, err)
		}
		text, err := d.readResume(ctx, in.UserID, p.ResumePath, gen)
		if errors.Is(err, errScan) {
			return importRead{Skip: err.Error()}, d.Web.SetResumeImport(ctx, in.UID,
				web.ResumeImport{Status: "failed", Reason: resumeScan, Generation: in.Generation, At: d.now()})
		}
		return importRead{Text: text}, err
	},
		hatchet.WithParents(memory),
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(2*time.Minute),
	)
	extract := wf.NewTask("extract", func(ctx hatchet.Context, in ResumeImportInput) (importExtract, error) {
		var r importRead
		if err := ctx.ParentOutput(read, &r); err != nil {
			return importExtract{}, err
		}
		return d.extractResume(ctx, in, r)
	},
		hatchet.WithParents(read),
		hatchet.WithSkipIf(hatchet.ParentCondition(read, skipped)),
		hatchet.WithRetries(1),
		hatchet.WithExecutionTimeout(5*time.Minute),
	)
	wf.NewTask("write", func(ctx hatchet.Context, in ResumeImportInput) (importWrite, error) {
		var p memoryProject
		var e importExtract
		if err := ctx.ParentOutput(memory, &p); err != nil {
			return importWrite{}, err
		}
		if err := ctx.ParentOutput(extract, &e); err != nil {
			return importWrite{}, err
		}
		return d.writeResume(ctx, in, p, e)
	},
		hatchet.WithParents(memory, read, extract),
		hatchet.WithSkipIf(hatchet.ParentCondition(read, skipped)),
		// The note is written whole, so a retry writes the same.
		hatchet.WithRetries(2),
		hatchet.WithExecutionTimeout(time.Minute),
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

// readResume returns the text of the given generation of the resume at
// path, read with pdftotext once per upload and kept in the user's jobs
// folder, apart from the chat workspace.
func (d Deps) readResume(ctx context.Context, userID, path string, gen int64) (string, error) {
	dir := filepath.Join(d.Agent.DataDir, "users", userID, "jobs")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	txt := filepath.Join(dir, "resume-"+strconv.FormatInt(gen, 10)+".txt")
	if b, err := os.ReadFile(txt); err == nil {
		return string(b), nil
	}
	pdf, err := os.CreateTemp(dir, "resume-*.pdf")
	if err != nil {
		return "", err
	}
	defer os.Remove(pdf.Name())
	err = d.Web.DownloadResume(ctx, path, gen, pdf)
	if cerr := pdf.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, orDefault(d.PDFToText, "pdftotext"), "-layout", "-enc", "UTF-8", pdf.Name(), "-")
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pdftotext: %v: %s", err, stderr.String())
	}
	text := cutText(stdout.String(), maxResume)
	if len(text) < 200 {
		return "", errScan
	}
	return text, os.WriteFile(txt, []byte(text), 0o600)
}

// extractResume has the agent list the resume's facts, with no tools: a
// resume is a document, read as text.
func (d Deps) extractResume(ctx context.Context, in ResumeImportInput, r importRead) (importExtract, error) {
	var out importExtract
	u, err := d.Store.UserByID(ctx, in.UserID)
	if err != nil || u == nil {
		return out, fmt.Errorf("user %s: %v", in.UserID, err)
	}
	name := orDefault(u.DisplayName, "the user")
	prompt, err := agent.ExtractResumePrompt(agent.ResumeReading{UserName: name, Date: d.today(), Resume: r.Text})
	if err != nil {
		return out, err
	}
	turn, err := agent.RunTurn(ctx, d.Agent, protocol.TurnInput{
		UserID: in.UserID, Text: prompt, UserName: name,
		Tools: protocol.ToolsNone, OutputSchema: agent.ExtractResumeSchema,
	}, func(protocol.Packet) {})
	out.CostUSD = turn.CostUSD
	if err != nil {
		return out, err
	}
	if err := json.Unmarshal(turn.Structured, &out.Resume); err != nil {
		return out, fmt.Errorf("resume: %w", err)
	}
	if len(out.Resume.Experience) == 0 && len(out.Resume.Education) == 0 {
		return out, fmt.Errorf("the resume lists no roles or education")
	}
	return out, nil
}

// writeResume merges the resume into the user's own Person note, creating
// it for a user who has none, and tells the web app.
func (d Deps) writeResume(ctx context.Context, in ResumeImportInput, p memoryProject, e importExtract) (importWrite, error) {
	self, err := d.Memory.Self(p.Project)
	if err != nil {
		return importWrite{}, err
	}
	title := ""
	if self != nil {
		title = self.Title
	} else {
		u, err := d.Store.UserByID(ctx, in.UserID)
		if err != nil || u == nil {
			return importWrite{}, fmt.Errorf("user %s: %v", in.UserID, err)
		}
		title = orDefault(u.DisplayName, u.Email)
	}
	r := e.Resume
	note := notes.SelfNote(self, notes.Resume{
		Contact: r.Contact, Location: r.Location, Experience: r.Experience, Education: r.Education,
		Skill: r.Skill, Project: r.Project, Publication: r.Publication,
	}, "stated, resume, "+d.today())
	if err := d.Memory.WriteNote(ctx, p.Project, notes.SelfFolder, title, "person", note); err != nil {
		return importWrite{}, err
	}
	slog.Info("resume imported", "user", in.UserID, "note", title)
	return importWrite{Note: title}, d.Web.SetResumeImport(ctx, in.UID, web.ResumeImport{Status: "done", Generation: in.Generation, At: d.now()})
}

// today is the local date, YYYY-MM-DD.
func (d Deps) today() string {
	loc := d.Location
	if loc == nil {
		loc = time.Local
	}
	return d.now().In(loc).Format(time.DateOnly)
}
