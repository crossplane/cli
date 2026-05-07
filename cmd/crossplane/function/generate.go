/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package function

import (
	"archive/tar"
	"bytes"
	"context"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/google/go-containerregistry/pkg/name"
	"github.com/spf13/afero"
	"github.com/spf13/afero/tarfs"
	"golang.org/x/mod/module"
	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/crossplane/crossplane-runtime/v2/pkg/errors"
	"github.com/crossplane/crossplane-runtime/v2/pkg/xpkg"

	v1alpha1 "github.com/crossplane/cli/v2/apis/dev/v1alpha1"
	"github.com/crossplane/cli/v2/internal/filesystem"
	"github.com/crossplane/cli/v2/internal/kcl"
	"github.com/crossplane/cli/v2/internal/project/projectfile"
	"github.com/crossplane/cli/v2/internal/schemas/generator"
	"github.com/crossplane/cli/v2/internal/schemas/manager"
	"github.com/crossplane/cli/v2/internal/schemas/runner"
	"github.com/crossplane/cli/v2/internal/terminal"
)

var (
	//go:embed templates/kcl/*
	kclTemplates embed.FS
	//go:embed all:templates/python
	pythonTemplates embed.FS
	//go:embed templates/go-templating/*
	goTemplatingTemplates embed.FS

	// The go template contains a go.mod, so we can't embed it as an
	// embed.FS. Instead we have to embed it as a tar archive and extract it
	// in code.
	//go:embed templates/go.tar
	goTemplate []byte
)

type generateCmd struct {
	Name         string `arg:""                            help:"Name of the function to generate. Must be a valid DNS-1035 label."`
	PipelinePath string `arg:""                            help:"Path to a Composition YAML file to add a pipeline step to."        optional:""`
	Language     string `default:"go-templating"           enum:"go,go-templating,kcl,python"                                       help:"Language to use for the function." short:"l"`
	ProjectFile  string `default:"crossplane-project.yaml" help:"Path to project definition file."                                  short:"f"`

	projFS            afero.Fs
	functionsFS       afero.Fs
	schemasFS         afero.Fs
	proj              *v1alpha1.Project
	fsPath            string
	projectRepository string
	projectSource     string
}

// AfterApply sets up the project filesystem.
func (c *generateCmd) AfterApply() error {
	if errs := validation.IsDNS1035Label(c.Name); len(errs) > 0 {
		return errors.Errorf("invalid function name %q: %s", c.Name, strings.Join(errs, "; "))
	}

	projFilePath, err := filepath.Abs(c.ProjectFile)
	if err != nil {
		return err
	}
	projDirPath := filepath.Dir(projFilePath)
	c.projFS = afero.NewBasePathFs(afero.NewOsFs(), projDirPath)

	proj, err := projectfile.Parse(c.projFS, filepath.Base(c.ProjectFile))
	if err != nil {
		return err
	}
	c.proj = proj

	c.functionsFS = afero.NewBasePathFs(c.projFS, proj.Spec.Paths.Functions)
	c.schemasFS = afero.NewBasePathFs(c.projFS, proj.Spec.Paths.Schemas)
	c.fsPath = path.Join(proj.Spec.Paths.Functions, c.Name)
	c.projectRepository = proj.Spec.Repository
	c.projectSource = proj.Spec.Source
	return nil
}

// Run generates a function scaffold.
func (c *generateCmd) Run(sp terminal.SpinnerPrinter) error {
	if err := c.validatePaths(); err != nil {
		return err
	}

	ctx := context.Background()
	apisFS := afero.NewBasePathFs(c.projFS, c.proj.Spec.Paths.APIs)
	if c.proj.Spec.Paths.APIs == "/" {
		apisFS = c.projFS
	}
	schemaMgr := manager.New(
		c.schemasFS,
		generator.AllLanguages(),
		runner.NewRealSchemaRunner(runner.WithImageConfig(c.proj.Spec.ImageConfigs)),
	)

	if err := sp.WrapWithSuccessSpinner("Generating schemas", func() error {
		_, err := schemaMgr.Generate(ctx, manager.NewFSSource(c.proj.Spec.Paths.APIs, apisFS))
		return err
	}); err != nil {
		return errors.Wrap(err, "failed to generate schemas")
	}

	type generatorFunc func(afero.Fs) error
	generators := map[string]generatorFunc{
		"go":            c.generateGoFiles,
		"go-templating": c.generateGoTemplatingFiles,
		"kcl":           c.generateKCLFiles,
		"python":        c.generatePythonFiles,
	}

	generator, ok := generators[c.Language]
	if !ok {
		return errors.Errorf("unsupported language %q", c.Language)
	}

	memFS := afero.NewMemMapFs()
	if err := sp.WrapWithSuccessSpinner(fmt.Sprintf("Generating %s function", c.Language), func() error {
		return generator(memFS)
	}); err != nil {
		return errors.Wrap(err, "cannot generate function files")
	}

	if err := sp.WrapWithSuccessSpinner("Writing function files", func() error {
		if err := copyFiles(memFS, c.functionsFS, c.Name); err != nil {
			return errors.Wrap(err, "cannot write function files")
		}

		if needsModelsSymlink(c.Language) {
			symlinkPath := filepath.Join(c.proj.Spec.Paths.Functions, c.Name, "model")
			schemasPath := filepath.Join(c.proj.Spec.Paths.Schemas, c.Language)

			projFS, ok := c.projFS.(*afero.BasePathFs)
			if !ok {
				return errors.Errorf("unexpected filesystem type %T for project", c.projFS)
			}

			if err := filesystem.CreateSymlink(projFS, symlinkPath, projFS, schemasPath); err != nil {
				return errors.Wrapf(err, "cannot create models symlink")
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if c.PipelinePath != "" {
		if err := sp.WrapWithSuccessSpinner("Adding pipeline step to composition", func() error {
			repo, err := name.NewRepository(c.projectRepository + "_" + c.Name)
			if err != nil {
				return errors.Wrapf(err, "cannot build function reference from repository %q", c.projectRepository)
			}
			functionRef := xpkg.ToDNSLabel(repo.RepositoryStr())
			return addStepToComposition(c.projFS, c.PipelinePath, c.Name, functionRef)
		}); err != nil {
			return errors.Wrap(err, "cannot add pipeline step to composition")
		}
	}

	return nil
}

func (c *generateCmd) validatePaths() error {
	if c.PipelinePath != "" {
		exists, err := afero.Exists(c.projFS, c.PipelinePath)
		if err != nil {
			return errors.Wrapf(err, "cannot check pipeline path %q", c.PipelinePath)
		}
		if !exists {
			return errors.Errorf("pipeline path %q does not exist", c.PipelinePath)
		}
	}

	exists, err := afero.DirExists(c.functionsFS, c.Name)
	if err != nil {
		return errors.Wrapf(err, "cannot check function directory %q", c.Name)
	}
	if exists {
		empty, err := afero.IsEmpty(c.functionsFS, c.Name)
		if err != nil {
			return errors.Wrapf(err, "cannot check function directory %q", c.Name)
		}
		if !empty {
			return errors.Errorf("function directory %q already exists and is not empty", c.Name)
		}
	}

	return nil
}

func needsModelsSymlink(language string) bool {
	return language == "kcl"
}

type kclTemplateData struct {
	ModName string
	Imports []kclImportStatement
}

type kclImportStatement struct {
	ImportPath string
	Alias      string
}

func (c *generateCmd) generateKCLFiles(fs afero.Fs) error {
	tmpls := template.Must(template.ParseFS(kclTemplates, "templates/kcl/*"))

	foundFolders, _ := filesystem.FindNestedFoldersWithPattern(c.schemasFS, "kcl", "*.k")

	existingAliases := make(map[string]bool)
	importStatements := make([]kclImportStatement, 0, len(foundFolders))
	for _, folder := range foundFolders {
		importPath, alias := kcl.FormatKclImportPath(folder, existingAliases)
		if importPath == "" {
			continue
		}
		importStatements = append(importStatements, kclImportStatement{
			ImportPath: importPath,
			Alias:      alias,
		})
	}

	tmplData := kclTemplateData{
		ModName: c.Name,
		Imports: importStatements,
	}

	return renderTemplates(fs, tmpls, tmplData)
}

type pythonTemplateData struct {
	HasSchemas  bool
	SchemasPath string
}

func (c *generateCmd) generatePythonFiles(targetFS afero.Fs) error {
	hasSchemas, _ := afero.DirExists(c.schemasFS, "python")
	if hasSchemas {
		entries, err := afero.ReadDir(c.schemasFS, "python")
		if err != nil {
			return errors.Wrap(err, "cannot read python schemas directory")
		}
		hasSchemas = len(entries) > 0
	}

	// Compute the relative path from the function dir to schemas/python/.
	fnDir := filepath.Join("/", c.proj.Spec.Paths.Functions, c.Name)
	relRoot, err := filepath.Rel(fnDir, "/")
	if err != nil {
		return errors.Wrap(err, "cannot determine path to schemas directory")
	}
	schemasPath := filepath.ToSlash(filepath.Join(relRoot, c.proj.Spec.Paths.Schemas, "python"))

	// template.ParseFS doesn't handle subdirectories, so we need to template
	// the top-level directory and the 'function' sub-directory separately.
	data := pythonTemplateData{
		HasSchemas:  hasSchemas,
		SchemasPath: schemasPath,
	}
	tmpls := template.Must(template.ParseFS(pythonTemplates, "templates/python/*.*"))
	if err := renderTemplates(targetFS, tmpls, data); err != nil {
		return err
	}

	if err := targetFS.Mkdir("function", 0o755); err != nil {
		return errors.Wrap(err, "cannot create function directory")
	}
	tmpls = template.Must(template.ParseFS(pythonTemplates, "templates/python/function/*.*"))
	return renderTemplates(afero.NewBasePathFs(targetFS, "function"), tmpls, data)
}

type goTemplateData struct {
	ModulePath string
	Imports    []goImport
}

type goImport struct {
	Module  string
	Version string
	Replace string
}

func (c *generateCmd) generateGoFiles(fs afero.Fs) error {
	source := strings.TrimPrefix(c.projectSource, "https://")
	goModPath := path.Join(source, "functions", c.Name)
	if source == "" || module.CheckPath(goModPath) != nil {
		goModPath = c.projectRepository + "/" + c.Name
	}
	if module.CheckPath(goModPath) != nil {
		goModPath = "project.example.com/functions/" + c.Name
	}

	// Compute relative path from the function dir to schemas/go/.
	fnDir := filepath.Join("/", c.proj.Spec.Paths.Functions, c.Name)
	relRoot, err := filepath.Rel(fnDir, "/")
	if err != nil {
		return errors.Wrap(err, "cannot determine path to models directory")
	}

	var imports []goImport
	schemasGoPath := filepath.Join(relRoot, c.proj.Spec.Paths.Schemas, "go")
	hasSchemas, _ := afero.DirExists(c.schemasFS, "go")
	if hasSchemas {
		imports = []goImport{{
			Module:  "dev.crossplane.io/models",
			Version: "v0.0.0",
			Replace: schemasGoPath,
		}}
	}

	tr := tar.NewReader(bytes.NewReader(goTemplate))
	templateFS := afero.NewIOFS(tarfs.New(tr))

	tmpls := template.Must(template.ParseFS(templateFS, "*"))
	tmplData := goTemplateData{
		ModulePath: goModPath,
		Imports:    imports,
	}

	return renderTemplates(fs, tmpls, tmplData)
}

type goTemplatingTemplateData struct {
	ModelIndexPath string
}

func (c *generateCmd) generateGoTemplatingFiles(fs afero.Fs) error {
	var modelPath string
	indexExists, _ := afero.Exists(c.schemasFS, "json/index.schema.json")
	if indexExists {
		var err error
		modelPath, err = filepath.Rel(c.fsPath, "schemas/json/index.schema.json")
		if err != nil {
			return errors.Wrap(err, "cannot determine model path")
		}
	}

	tmpls := template.Must(template.ParseFS(goTemplatingTemplates, "templates/go-templating/*"))
	tmplData := goTemplatingTemplateData{
		ModelIndexPath: modelPath,
	}

	return renderTemplates(fs, tmpls, tmplData)
}

func renderTemplates(targetFS afero.Fs, tmpls *template.Template, data any) error {
	for _, tmpl := range tmpls.Templates() {
		fname := tmpl.Name()
		// Strip .tmpl suffix from output filename.
		outName := strings.TrimSuffix(fname, ".tmpl")

		file, err := targetFS.Create(filepath.Clean(outName))
		if err != nil {
			return errors.Wrapf(err, "cannot create file %s", outName)
		}
		if err := tmpl.Execute(file, data); err != nil {
			return errors.Wrapf(err, "cannot render template %s", fname)
		}
		if err := file.Close(); err != nil {
			return errors.Wrapf(err, "cannot close file %s", outName)
		}
	}
	return nil
}

func copyFiles(src, dst afero.Fs, dstDir string) error {
	return afero.Walk(src, "", func(p string, info fs.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return dst.MkdirAll(path.Join(dstDir, p), 0o755)
		}
		data, err := afero.ReadFile(src, p)
		if err != nil {
			return err
		}
		return afero.WriteFile(dst, path.Join(dstDir, p), data, 0o644)
	})
}
