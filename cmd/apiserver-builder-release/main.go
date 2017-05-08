/*
Copyright 2017 The Kubernetes Authors.

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

package main

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io/ioutil"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

var targets = []string{}
var output string
var dobuild bool
var dofetch bool
var dovendor bool
var version string

var cachetooldir string
var cachevendordir string

var DefaultTargets = []string{"linux:amd64", "darwin:amd64", "windows:amd64"}

func main() {
	buildCmd.Flags().StringSliceVar(&targets, "targets",
		DefaultTargets, "GOOS:GOARCH pair.  maybe specified multiple times.")
	buildCmd.Flags().StringVar(&cachetooldir, "tooldir", "",
		"if specified, use this directory for building tools instead of creating a tmp directory.")
	buildCmd.Flags().StringVar(&cachevendordir, "vendordir", "",
		"if specified, use this directory for setting up vendor instead of creating a tmp directory.")
	buildCmd.Flags().StringVar(&output, "output", "apiserver-builder",
		"value name of the tar file to build")
	buildCmd.Flags().StringVar(&version, "version", "", "version name")

	buildCmd.Flags().BoolVar(&dobuild, "build", true, "if false, only build the go packages for the current os:arch")
	buildCmd.Flags().BoolVar(&dofetch, "fetch", true, "if true, fetch the go packages")
	buildCmd.Flags().BoolVar(&dovendor, "vendor", true, "if true, fetch packages to vendor")

	cmd.AddCommand(buildCmd)

	if err := cmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "%v", err)
		os.Exit(-1)
	}
}

var cmd = &cobra.Command{
	Use:   "apiserver-builder-release",
	Short: "apiserver-builder-release builds a .tar.gz release package",
	Long:  `apiserver-builder-release builds a .tar.gz release package`,
	Run:   RunMain,
}

func RunMain(cmd *cobra.Command, args []string) {
	cmd.Help()
}

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "build the binaries",
	Long:  `build the binaries`,
	Run:   RunBuild,
}

func TmpDir() string {
	dir, err := ioutil.TempDir(os.TempDir(), "apiserver-builder-release")
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create temp directory %s %v\n", dir, err)
		os.Exit(-1)
	}

	dir, err = filepath.EvalSymlinks(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(-1)
	}

	err = os.Mkdir(filepath.Join(dir, "src"), 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create directory %s %v\n", filepath.Join(dir, "src"), err)
		os.Exit(-1)
	}

	err = os.Mkdir(filepath.Join(dir, "bin"), 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create directory %s %v\n", filepath.Join(dir, "bin"), err)
		os.Exit(-1)
	}
	return dir
}

func RunBuild(cmd *cobra.Command, args []string) {
	if len(version) == 0 {
		fmt.Fprintf(os.Stderr, "must specify the --version flag")
		os.Exit(-1)
	}
	if len(targets) == 0 && dobuild {
		fmt.Fprintf(os.Stderr, "must provide at least one --targets flag when building tools")
		os.Exit(-1)
	}

	// Create a temporary build directory
	tooldir := cachetooldir
	if len(tooldir) == 0 {
		tooldir = TmpDir()
		fmt.Printf("to rerun with cached go fetch use `--tooldir %s`\n", tooldir)
	} else {
		// Make sure we aren't using a symlink, because when we create the tar file we don't
		// copy symlinks
		var err error
		tooldir, err = filepath.EvalSymlinks(tooldir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(-1)
		}
	}

	if dofetch {
		for _, pkg := range BuildPackages {
			Fetch(pkg, tooldir)
		}
	}

	vendor := ""
	if dovendor {
		//Build binaries for the current platform
		for _, pkg := range BuildPackages {
			Build(filepath.Join("src", pkg, "main.go"),
				filepath.Join("bin", filepath.Base(pkg)),
				"", "", tooldir,
			)
		}
		vendor = BuildVendor(tooldir)
	}

	// Build binaries for the targeted platforms in then tar
	for _, target := range targets {
		// Build binaries for this os:arch
		parts := strings.Split(target, ":")
		if len(parts) != 2 {
			fmt.Fprintf(os.Stderr, "--targets flags must be GOOS:GOARCH pairs [%s]\n", target)
			os.Exit(-1)
		}
		goos := parts[0]
		goarch := parts[1]
		if dobuild {
			// Cleanup old binaries
			os.RemoveAll(filepath.Join(tooldir, "bin"))
			err := os.Mkdir(filepath.Join(tooldir, "bin"), 0700)
			if err != nil {
				fmt.Fprintf(os.Stderr, "failed to create directory %s %v\n", filepath.Join(tooldir, "bin"), err)
				os.Exit(-1)
			}

			for _, pkg := range BuildPackages {
				Build(filepath.Join("src", pkg, "main.go"),
					filepath.Join("bin", filepath.Base(pkg)),
					goos, goarch, tooldir,
				)
			}
		}
		PackageTar(goos, goarch, tooldir, vendor)
	}
}

func RunCmd(cmd *exec.Cmd, gopath string) {
	gopath, err := filepath.Abs(gopath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(-1)
	}
	gopath, err = filepath.EvalSymlinks(gopath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(-1)
	}
	cmd.Env = append(cmd.Env, fmt.Sprintf("GOPATH=%s", gopath))
	cmd.Env = append(cmd.Env, os.Environ()...)
	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout
	if len(cmd.Dir) == 0 {
		cmd.Dir = gopath
	}
	fmt.Printf("%s\n", strings.Join(cmd.Args, " "))
	err = cmd.Run()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(-1)
	}
}

func Build(input, output, goos, goarch, dir string) {
	cmd := exec.Command("go", "build", "-o", output, input)

	// CGO_ENABLED=0 for statically compile binaries
	cmd.Env = append(cmd.Env, "CGO_ENABLED=0")
	if len(goos) > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("GOOS=%s", goos))
	}
	if len(goarch) > 0 {
		cmd.Env = append(cmd.Env, fmt.Sprintf("GOARCH=%s", goarch))
	}
	RunCmd(cmd, dir)
}

func Fetch(pkg, dir string) {
	RunCmd(exec.Command("go", "get", "-d", pkg), dir)
}

var BuildPackages = []string{
	"github.com/kubernetes-incubator/apiserver-builder/cmd/apiregister-gen",
	"github.com/kubernetes-incubator/apiserver-builder/cmd/apiserver-boot",
	"github.com/kubernetes-incubator/reference-docs/gen-apidocs",
	"k8s.io/kubernetes/cmd/libs/go2idl/client-gen",
	"k8s.io/kubernetes/cmd/libs/go2idl/conversion-gen",
	"k8s.io/kubernetes/cmd/libs/go2idl/deepcopy-gen",
	"k8s.io/kubernetes/cmd/libs/go2idl/defaulter-gen",
	"k8s.io/kubernetes/cmd/libs/go2idl/informer-gen",
	"k8s.io/kubernetes/cmd/libs/go2idl/lister-gen",
	"k8s.io/kubernetes/cmd/libs/go2idl/openapi-gen",
}

func PackageTar(goos, goarch, tooldir, vendordir string) {
	// create the new file
	fw, err := os.Create(fmt.Sprintf("%s-%s-%s-%s.tar.gz", output, version, goos, goarch))
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create output file %s %v\n", output, err)
		os.Exit(-1)
	}
	defer fw.Close()

	// setup gzip of tar
	gw := gzip.NewWriter(fw)
	defer gw.Close()

	// setup tar writer
	tw := tar.NewWriter(gw)
	defer tw.Close()

	// Add all of the bin files
	filepath.Walk(filepath.Join(tooldir, "bin"), TarFile{
		tw,
		0555,
		tooldir,
		"",
	}.Do)

	// Add all of the src files
	tf := TarFile{
		tw,
		0644,
		vendordir,
		"src",
	}
	filepath.Walk(filepath.Join(vendordir, "vendor"), tf.Do)
	tf.Write(filepath.Join(vendordir, "glide.yaml"))
	tf.Write(filepath.Join(vendordir, "glide.lock"))
}

type TarFile struct {
	Writer *tar.Writer
	Mode   int64
	Root   string
	Parent string
}

func (t TarFile) Do(path string, info os.FileInfo, err error) error {
	if info.IsDir() {
		return nil
	}

	eval, err := filepath.EvalSymlinks(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(-1)
	}
	if eval != path {
		name := strings.Replace(path, t.Root, "", -1)
		if len(t.Parent) != 0 {
			name = filepath.Join(t.Parent, name)
		}
		linkName := strings.Replace(eval, t.Root, "", -1)
		if len(t.Parent) != 0 {
			linkName = filepath.Join(t.Parent, linkName)
		}
		hdr := &tar.Header{
			Name:     name,
			Mode:     t.Mode,
			Linkname: linkName,
		}
		if err := t.Writer.WriteHeader(hdr); err != nil {
			fmt.Fprintf(os.Stderr, "failed to write output for %s %v\n", path, err)
			os.Exit(-1)
		}
		return nil
	}

	return t.Write(path)
}

func (t TarFile) Write(path string) error {
	// Get the relative name of the file
	name := strings.Replace(path, t.Root, "", -1)
	if len(t.Parent) != 0 {
		name = filepath.Join(t.Parent, name)
	}
	body, err := ioutil.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to read file %s %v\n", path, err)
		os.Exit(-1)
	}
	if len(body) == 0 {
		return nil
	}

	hdr := &tar.Header{
		Name: name,
		Mode: t.Mode,
		Size: int64(len(body)),
	}
	if err := t.Writer.WriteHeader(hdr); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write output for %s %v\n", path, err)
		os.Exit(-1)
	}
	if _, err := t.Writer.Write(body); err != nil {
		fmt.Fprintf(os.Stderr, "failed to write output for %s %v\n", path, err)
		os.Exit(-1)
	}
	return nil
}

func BuildVendor(tooldir string) string {
	vendordir := cachevendordir
	if len(vendordir) == 0 {
		vendordir = TmpDir()
		fmt.Printf("to rerun with cached glide use `--vendordir %s`\n", vendordir)
	}

	vendordir, err := filepath.EvalSymlinks(vendordir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(-1)
	}

	pkgDir := filepath.Join(vendordir, "src", "github.com", "kubernetes-incubator", "test")
	bootBin := filepath.Join(tooldir, "bin", "apiserver-boot")
	err = os.MkdirAll(pkgDir, 0700)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to create directory %s %v\n", pkgDir, err)
		os.Exit(-1)
	}

	ioutil.WriteFile(filepath.Join(pkgDir, "boilerplate.go.txt"), []byte(""), 0555)

	cmd := exec.Command(bootBin, "init", "--domain", "k8s.io")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	cmd = exec.Command(bootBin, "create-group", "--domain", "k8s.io", "--group", "misk")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	cmd = exec.Command(bootBin, "create-version", "--domain", "k8s.io", "--group", "misk", "--version", "v1beta1")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	cmd = exec.Command(bootBin, "create-resource", "--domain", "k8s.io", "--group", "misk", "--version", "v1beta1", "--kind", "Student", "--resource", "students")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	cmd = exec.Command(bootBin, "glide-install", "--fetch")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	cmd = exec.Command(bootBin, "generate", "--api-versions", "misk/v1beta1")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	cmd = exec.Command("go", "build", "main.go")
	cmd.Dir = pkgDir
	RunCmd(cmd, vendordir)

	return pkgDir
}
