// Package workflow compiles Python workflow graphs and provides durable simulator
// state. Its transitions do not execute agent, command, or worktree side effects.
package workflow

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"testing/fstest"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed harness.py
var sdk []byte

//go:embed output_model.py
var outputSDK []byte

// Compile runs only workflow graph authoring inside WASI. The runtime directory
// contains python.wasm and lib; SDK files are embedded in the Go binary. It does
// not mount the repository, invoke a host Python, or write to terminal streams.
func Compile(ctx context.Context, scriptPath, runtimeDir, skillsModule string) (Graph, error) {
	source, err := os.ReadFile(scriptPath)
	if err != nil {
		return Graph{}, err
	}
	wasm, err := os.ReadFile(runtimeDir + "/python.wasm")
	if err != nil {
		return Graph{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	runtime := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(4096).WithCloseOnContextDone(true))
	defer runtime.Close(context.Background())
	if _, err = wasi_snapshot_preview1.Instantiate(ctx, runtime); err != nil {
		return Graph{}, err
	}
	var output, diagnostics bytes.Buffer
	files := fstest.MapFS{
		"harness.py":      &fstest.MapFile{Data: sdk},
		"workflow.py":     &fstest.MapFile{Data: source},
		"output_model.py": &fstest.MapFile{Data: outputSDK},
	}
	if skillsModule != "" {
		module, err := os.ReadFile(skillsModule)
		if err != nil {
			return Graph{}, err
		}
		files["generated_skills.py"] = &fstest.MapFile{Data: module}
	}
	fs := wazero.NewFSConfig().WithFSMount(os.DirFS(runtimeDir), "/python").WithFSMount(files, "/workflow")
	_, err = runtime.InstantiateWithConfig(ctx, wasm, wazero.NewModuleConfig().WithFSConfig(fs).
		WithArgs("python", "-B", "/workflow/workflow.py").WithEnv("PYTHONHOME", "/python").
		WithStdout(&output).WithStderr(&diagnostics).WithSysWalltime().WithSysNanotime())
	if err != nil {
		return Graph{}, fmt.Errorf("CPython WASI: %w\n%s", err, diagnostics.String())
	}
	var graph Graph
	if err = json.Unmarshal(output.Bytes(), &graph); err != nil {
		return Graph{}, fmt.Errorf("workflow must export one JSON document: %w", err)
	}
	if err = Validate(graph); err != nil {
		return Graph{}, err
	}
	return graph, nil
}
