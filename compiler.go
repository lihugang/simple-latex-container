package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type CompileResult struct {
	ID      string `json:"id"`
	PageNum int    `json:"pagenum"`
}

type CompileFailure struct {
	Message string
}

type CompileProcessor interface {
	Process(ctx context.Context, payload string) (CompileResult, *CompileFailure, error)
}

type CommandRunner interface {
	CombinedOutput(ctx context.Context, dir string, name string, args ...string) ([]byte, error)
}

type PDFInspector interface {
	PageCount(ctx context.Context, pdfPath string) (int, error)
}

type ExecCommandRunner struct{}

func (ExecCommandRunner) CombinedOutput(ctx context.Context, dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

type PDFInfoInspector struct {
	Runner CommandRunner
}

func (p PDFInfoInspector) PageCount(ctx context.Context, pdfPath string) (int, error) {
	output, err := p.Runner.CombinedOutput(ctx, "", "pdfinfo", pdfPath)
	if err != nil {
		return 0, fmt.Errorf("run pdfinfo: %w: %s", err, strings.TrimSpace(string(output)))
	}

	for _, line := range strings.Split(string(output), "\n") {
		if !strings.HasPrefix(line, "Pages:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "Pages:"))
		pages, err := strconv.Atoi(value)
		if err != nil {
			return 0, fmt.Errorf("parse pdfinfo pages value %q: %w", value, err)
		}
		if pages <= 0 {
			return 0, fmt.Errorf("pdfinfo returned invalid page count %d", pages)
		}
		return pages, nil
	}

	return 0, errors.New("pdfinfo output missing Pages field")
}

type Compiler struct {
	resultsDir string
	tempRoot   string
	runner     CommandRunner
	inspector  PDFInspector
	locks      *KeyedLocker
}

func NewCompiler(resultsDir, tempRoot string, runner CommandRunner, inspector PDFInspector) *Compiler {
	return &Compiler{
		resultsDir: resultsDir,
		tempRoot:   tempRoot,
		runner:     runner,
		inspector:  inspector,
		locks:      NewKeyedLocker(),
	}
}

func (c *Compiler) Process(ctx context.Context, payload string) (CompileResult, *CompileFailure, error) {
	sum := sha256.Sum256([]byte(payload))
	id := hex.EncodeToString(sum[:])

	unlock := c.locks.Lock(id)
	defer unlock()

	if result, ok, err := c.cachedResult(ctx, id); err != nil {
		return CompileResult{}, nil, err
	} else if ok {
		return result, nil, nil
	}

	return c.compile(ctx, id, payload)
}

func (c *Compiler) cachedResult(ctx context.Context, id string) (CompileResult, bool, error) {
	resultDir := filepath.Join(c.resultsDir, id)
	pdfPath := filepath.Join(resultDir, "main.pdf")
	if _, err := os.Stat(pdfPath); err != nil {
		if os.IsNotExist(err) {
			return CompileResult{}, false, nil
		}
		return CompileResult{}, false, fmt.Errorf("stat cached pdf: %w", err)
	}

	pages, err := c.inspector.PageCount(ctx, pdfPath)
	if err != nil {
		return CompileResult{}, false, nil
	}

	for page := 1; page <= pages; page++ {
		pngPath := filepath.Join(resultDir, fmt.Sprintf("%d.png", page))
		if _, err := os.Stat(pngPath); err != nil {
			if os.IsNotExist(err) {
				return CompileResult{}, false, nil
			}
			return CompileResult{}, false, fmt.Errorf("stat cached png %d: %w", page, err)
		}
	}

	return CompileResult{ID: id, PageNum: pages}, true, nil
}

func (c *Compiler) compile(ctx context.Context, id, payload string) (CompileResult, *CompileFailure, error) {
	tmpDir := filepath.Join(c.tempRoot, id)
	if err := os.RemoveAll(tmpDir); err != nil {
		return CompileResult{}, nil, fmt.Errorf("clear temp dir: %w", err)
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return CompileResult{}, nil, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	texPath := filepath.Join(tmpDir, "main.tex")
	if err := os.WriteFile(texPath, []byte(payload), 0o644); err != nil {
		return CompileResult{}, nil, fmt.Errorf("write tex file: %w", err)
	}

	output, err := c.runner.CombinedOutput(ctx, tmpDir, "xelatex", "-interaction=nonstopmode", "-no-shell-escape", "main.tex")
	if err != nil {
		return CompileResult{}, &CompileFailure{Message: string(output)}, nil
	}

	pdfPath := filepath.Join(tmpDir, "main.pdf")
	pages, err := c.inspector.PageCount(ctx, pdfPath)
	if err != nil {
		return CompileResult{}, nil, fmt.Errorf("inspect compiled pdf: %w", err)
	}

	convertOutput, err := c.runner.CombinedOutput(ctx, tmpDir, "pdftoppm", "-png", "main.pdf", "page")
	if err != nil {
		return CompileResult{}, nil, fmt.Errorf("convert pdf to png: %w: %s", err, strings.TrimSpace(string(convertOutput)))
	}

	stageDir := filepath.Join(c.resultsDir, "."+id+".stage."+strconv.FormatInt(time.Now().UnixNano(), 10))
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return CompileResult{}, nil, fmt.Errorf("create stage dir: %w", err)
	}

	stageCleanup := true
	defer func() {
		if stageCleanup {
			_ = os.RemoveAll(stageDir)
		}
	}()

	if err := copyFile(texPath, filepath.Join(stageDir, "main.tex")); err != nil {
		return CompileResult{}, nil, fmt.Errorf("copy tex file: %w", err)
	}
	if err := copyFile(pdfPath, filepath.Join(stageDir, "main.pdf")); err != nil {
		return CompileResult{}, nil, fmt.Errorf("copy pdf file: %w", err)
	}
	for page := 1; page <= pages; page++ {
		src := filepath.Join(tmpDir, fmt.Sprintf("page-%d.png", page))
		dst := filepath.Join(stageDir, fmt.Sprintf("%d.png", page))
		if err := copyFile(src, dst); err != nil {
			return CompileResult{}, nil, fmt.Errorf("copy png page %d: %w", page, err)
		}
	}

	finalDir := filepath.Join(c.resultsDir, id)
	if err := os.MkdirAll(c.resultsDir, 0o755); err != nil {
		return CompileResult{}, nil, fmt.Errorf("create results dir: %w", err)
	}
	if err := os.RemoveAll(finalDir); err != nil {
		return CompileResult{}, nil, fmt.Errorf("clear result dir: %w", err)
	}
	if err := os.Rename(stageDir, finalDir); err != nil {
		return CompileResult{}, nil, fmt.Errorf("publish result dir: %w", err)
	}
	stageCleanup = false

	return CompileResult{ID: id, PageNum: pages}, nil, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	return nil
}

type KeyedLocker struct {
	mu    sync.Mutex
	locks map[string]*lockRef
}

type lockRef struct {
	mu   sync.Mutex
	refs int
}

func NewKeyedLocker() *KeyedLocker {
	return &KeyedLocker{locks: make(map[string]*lockRef)}
}

func (k *KeyedLocker) Lock(key string) func() {
	k.mu.Lock()
	ref := k.locks[key]
	if ref == nil {
		ref = &lockRef{}
		k.locks[key] = ref
	}
	ref.refs++
	k.mu.Unlock()

	ref.mu.Lock()

	return func() {
		ref.mu.Unlock()

		k.mu.Lock()
		defer k.mu.Unlock()
		ref.refs--
		if ref.refs == 0 {
			delete(k.locks, key)
		}
	}
}
