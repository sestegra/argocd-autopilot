package application

import (
	"fmt"
	"io/ioutil"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/argoproj-labs/argocd-autopilot/pkg/fs"
	fsmocks "github.com/argoproj-labs/argocd-autopilot/pkg/fs/mocks"
	"github.com/argoproj-labs/argocd-autopilot/pkg/kube"
	"github.com/argoproj-labs/argocd-autopilot/pkg/store"

	"github.com/go-git/go-billy/v5/memfs"
	billyUtils "github.com/go-git/go-billy/v5/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	kusttypes "sigs.k8s.io/kustomize/api/types"
)

func Test_newKustApp(t *testing.T) {
	orgGenerateManifests := generateManifests
	defer func() { generateManifests = orgGenerateManifests }()
	generateManifests = func(k *kusttypes.Kustomization) ([]byte, error) {
		return []byte("foo"), nil
	}

	tests := map[string]struct {
		run               bool
		opts              *CreateOptions
		srcRepoURL        string
		srcTargetRevision string
		projectName       string
		wantErr           string
		assertFn          func(*testing.T, *kustApp)
	}{
		"No app specifier": {
			opts: &CreateOptions{
				AppName: "name",
			},
			wantErr: ErrEmptyAppSpecifier.Error(),
		},
		"No app name": {
			opts: &CreateOptions{
				AppSpecifier: "app",
			},
			wantErr: ErrEmptyAppName.Error(),
		},
		"No project name": {
			opts: &CreateOptions{
				AppSpecifier: "app",
				AppName:      "name",
			},
			wantErr: ErrEmptyProjectName.Error(),
		},
		"Invalid installation mode": {
			opts: &CreateOptions{
				AppSpecifier:     "app",
				AppName:          "name",
				InstallationMode: "foo",
			},
			projectName: "project",
			wantErr:     "unknown installation mode: foo",
		},
		"Normal installation mode": {
			opts: &CreateOptions{
				AppSpecifier: "app",
				AppName:      "name",
			},
			srcRepoURL:        "github.com/owner/repo",
			srcTargetRevision: "branch",
			projectName:       "project",
			assertFn: func(t *testing.T, a *kustApp) {
				assert.Equal(t, "app", a.base.Resources[0])
				assert.Equal(t, "../../base", a.overlay.Resources[0])
				assert.Nil(t, a.namespace)
				assert.Nil(t, a.manifests)
				assert.True(t, reflect.DeepEqual(&Config{
					AppName:           "name",
					UserGivenName:     "name",
					SrcPath:           filepath.Join(store.Default.AppsDir, "name", store.Default.OverlaysDir, "project"),
					SrcRepoURL:        "github.com/owner/repo",
					SrcTargetRevision: "branch",
				}, a.config))
			},
		},
		"Flat installation mode with namespace": {
			run: true,
			opts: &CreateOptions{
				AppSpecifier:     "app",
				AppName:          "name",
				InstallationMode: InstallationModeFlat,
				DestNamespace:    "namespace",
			},
			srcRepoURL:        "github.com/owner/repo",
			srcTargetRevision: "branch",
			projectName:       "project",
			assertFn: func(t *testing.T, a *kustApp) {
				assert.Equal(t, "install.yaml", a.base.Resources[0])
				assert.Equal(t, []byte("foo"), a.manifests)
				assert.Equal(t, "../../base", a.overlay.Resources[0])
				assert.Equal(t, "namespace.yaml", a.overlay.Resources[1])
				assert.Equal(t, "namespace", a.namespace.ObjectMeta.Name)
				assert.True(t, reflect.DeepEqual(&Config{
					AppName:           "name",
					UserGivenName:     "name",
					DestNamespace:     "namespace",
					SrcPath:           filepath.Join(store.Default.AppsDir, "name", store.Default.OverlaysDir, "project"),
					SrcRepoURL:        "github.com/owner/repo",
					SrcTargetRevision: "branch",
				}, a.config))
			},
		},
	}
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			app, err := newKustApp(tt.opts, tt.projectName, tt.srcRepoURL, tt.srcTargetRevision)
			if err != nil {
				if tt.wantErr != "" {
					assert.EqualError(t, err, tt.wantErr)
				} else {
					t.Errorf("prepare() error = %v", err)
				}

				return
			}

			tt.assertFn(t, app)
		})
	}
}

func Test_writeFile(t *testing.T) {
	type args struct {
		root string
		path string
		name string
		data []byte
	}
	tests := map[string]struct {
		args     args
		assertFn func(t *testing.T, repofs fs.FS, exists bool, err error)
		beforeFn func(repofs fs.FS) fs.FS
	}{
		"On Root": {
			args: args{
				path: "foo/bar",
				name: "test",
				data: []byte("data"),
			},
			assertFn: func(t *testing.T, repofs fs.FS, exists bool, ret error) {
				assert.NoError(t, ret)

				f, err := repofs.Open("/foo/bar")
				assert.NoError(t, err)
				d, err := ioutil.ReadAll(f)
				assert.NoError(t, err)

				assert.Equal(t, d, []byte("data"))
				assert.False(t, exists)
			},
		},
		"With Chroot": {
			args: args{
				root: "someroot",
				path: "foo/bar",
				name: "test",
				data: []byte("data2"),
			},
			assertFn: func(t *testing.T, repofs fs.FS, exists bool, ret error) {
				assert.NoError(t, ret)

				assert.Equal(t, "/someroot", repofs.Root())
				f, err := repofs.Open("/foo/bar")
				assert.NoError(t, err)
				d, err := ioutil.ReadAll(f)
				assert.NoError(t, err)

				assert.Equal(t, d, []byte("data2"))
				assert.False(t, exists)
			},
		},
		"File exists": {
			args: args{
				path: "foo/bar",
				name: "test",
				data: []byte("data2"),
			},
			beforeFn: func(repofs fs.FS) fs.FS {
				_ = billyUtils.WriteFile(repofs, "/foo/bar", []byte("data"), 0666)
				return repofs
			},
			assertFn: func(t *testing.T, _ fs.FS, exists bool, ret error) {
				assert.NoError(t, ret)
				assert.True(t, exists)
			},
		},
		"Write error": {
			args: args{
				path: "foo/bar",
				name: "test",
				data: []byte("data2"),
			},
			beforeFn: func(_ fs.FS) fs.FS {
				mfs := &fsmocks.FS{}
				mfs.On("CheckExistsOrWrite", mock.Anything, mock.Anything).Return(false, fmt.Errorf("error"))
				mfs.On("Root").Return("/")
				mfs.On("Join", mock.Anything, mock.Anything).Return("/foo/bar")
				return mfs
			},
			assertFn: func(t *testing.T, _ fs.FS, _ bool, ret error) {
				assert.Error(t, ret)
				assert.EqualError(t, ret, "failed to create 'test' file at '/foo/bar': error")
			},
		},
	}
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			repofs := fs.Create(memfs.New())
			if tt.args.root != "" {
				bfs, _ := repofs.Chroot(tt.args.root)
				repofs = fs.Create(bfs)
			}

			if tt.beforeFn != nil {
				repofs = tt.beforeFn(repofs)
			}

			got, err := writeFile(repofs, tt.args.path, tt.args.name, tt.args.data)
			tt.assertFn(t, repofs, got, err)
		})
	}
}

func Test_kustCreateFiles(t *testing.T) {
	tests := map[string]struct {
		beforeFn func() (*kustApp, fs.FS, string)
		assertFn func(*testing.T, fs.FS, error)
	}{
		"Should create all files for a simple application": {
			beforeFn: func() (*kustApp, fs.FS, string) {
				app := &kustApp{
					baseApp: baseApp{
						opts: &CreateOptions{
							AppName: "app",
						},
					},
				}
				return app, fs.Create(memfs.New()), "project"
			},
			assertFn: func(t *testing.T, repofs fs.FS, err error) {
				assert.NoError(t, err)
				assert.True(t, repofs.ExistsOrDie(store.Default.AppsDir), "kustomization dir should exist")
				assert.True(t, repofs.ExistsOrDie(repofs.Join(store.Default.AppsDir, "app", store.Default.BaseDir, "kustomization.yaml")), "base kustomization should exist")
				assert.False(t, repofs.ExistsOrDie(repofs.Join(store.Default.AppsDir, "app", store.Default.BaseDir, "install.yaml")), "install file should not exist")
				assert.True(t, repofs.ExistsOrDie(repofs.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project", "kustomization.yaml")), "overlay kustomization should exist")
				assert.True(t, repofs.ExistsOrDie(repofs.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project", "config.json")), "overlay config should exist")
				assert.False(t, repofs.ExistsOrDie(repofs.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project", "namespace.yaml")), "overlay namespace should not exist")
			},
		},
		"Should create install.yaml when manifests exist": {
			beforeFn: func() (*kustApp, fs.FS, string) {
				app := &kustApp{
					baseApp: baseApp{
						opts: &CreateOptions{
							AppName: "app",
						},
					},
					manifests: []byte("some manifests"),
				}
				return app, fs.Create(memfs.New()), "project"
			},
			assertFn: func(t *testing.T, repofs fs.FS, err error) {
				assert.NoError(t, err)
				installFile := repofs.Join(store.Default.AppsDir, "app", store.Default.BaseDir, "install.yaml")
				assert.True(t, repofs.ExistsOrDie(installFile), "install file should exist")
				data, _ := repofs.ReadFile(installFile)
				assert.Equal(t, "some manifests", string(data))
			},
		},
		"Should create namespace.yaml when needed": {
			beforeFn: func() (*kustApp, fs.FS, string) {
				app := &kustApp{
					baseApp: baseApp{
						opts: &CreateOptions{
							AppName: "app",
						},
					},
					namespace: kube.GenerateNamespace("namespace"),
				}
				return app, fs.Create(memfs.New()), "project"
			},
			assertFn: func(t *testing.T, repofs fs.FS, err error) {
				assert.NoError(t, err)
				assert.True(t, repofs.ExistsOrDie(repofs.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project", "namespace.yaml")), "overlay namespace should exist")
			},
		},
		"Should fail when base kustomization is different from kustRes": {
			beforeFn: func() (*kustApp, fs.FS, string) {
				app := &kustApp{
					baseApp: baseApp{
						opts: &CreateOptions{
							AppName: "app",
						},
					},
					base: &kusttypes.Kustomization{
						TypeMeta: kusttypes.TypeMeta{
							APIVersion: kusttypes.KustomizationVersion,
							Kind:       kusttypes.KustomizationKind,
						},
						Resources: []string{"github.com/owner/repo?ref=v1.2.3"},
					},
				}
				repofs := fs.Create(memfs.New())
				origBase := &kusttypes.Kustomization{
					TypeMeta: kusttypes.TypeMeta{
						APIVersion: kusttypes.KustomizationVersion,
						Kind:       kusttypes.KustomizationKind,
					},
					Resources: []string{"github.com/owner/different_repo?ref=v1.2.3"},
				}
				_ = repofs.WriteYamls(repofs.Join(store.Default.AppsDir, "app", store.Default.BaseDir, "kustomization.yaml"), origBase)
				return app, repofs, "project"
			},
			assertFn: func(t *testing.T, _ fs.FS, err error) {
				assert.ErrorIs(t, err, ErrAppCollisionWithExistingBase)
			},
		},
		"Should fail when overlay already exists": {
			beforeFn: func() (*kustApp, fs.FS, string) {
				app := &kustApp{
					baseApp: baseApp{
						opts: &CreateOptions{
							AppName: "app",
						},
					},
				}
				repofs := fs.Create(memfs.New())
				origBase := &kusttypes.Kustomization{
					TypeMeta: kusttypes.TypeMeta{
						APIVersion: kusttypes.KustomizationVersion,
						Kind:       kusttypes.KustomizationKind,
					},
					Resources: []string{"github.com/owner/different_repo?ref=v1.2.3"},
				}
				_ = repofs.WriteYamls(repofs.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project", "kustomization.yaml"), origBase)
				return app, repofs, "project"
			},
			assertFn: func(t *testing.T, _ fs.FS, err error) {
				assert.ErrorIs(t, err, ErrAppAlreadyInstalledOnProject)
			},
		},
	}
	for tname, tt := range tests {
		t.Run(tname, func(t *testing.T) {
			app, repofs, projectName := tt.beforeFn()
			err := app.CreateFiles(repofs, projectName)
			tt.assertFn(t, repofs, err)
		})
	}
}

func TestInferAppType(t *testing.T) {
	tests := map[string]struct {
		want     string
		beforeFn func() fs.FS
	}{
		"Should return ksonnet if required files are present": {
			want: "ksonnet",
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "app.yaml", []byte{}, 0666)
				_ = billyUtils.WriteFile(memfs, "components/params.libsonnet", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should not return ksonnet if 'app.yaml' is missing": {
			want: AppTypeDirectory,
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "components/params.libsonnet", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should not return ksonnet if 'components/params.libsonnet' is missing": {
			want: AppTypeDirectory,
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "app.yaml", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should return ksonnet as the highest priority": {
			want: "ksonnet",
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "app.yaml", []byte{}, 0666)
				_ = billyUtils.WriteFile(memfs, "components/params.libsonnet", []byte{}, 0666)
				_ = billyUtils.WriteFile(memfs, "Chart.yaml", []byte{}, 0666)
				_ = billyUtils.WriteFile(memfs, "kustomization.yaml", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should return helm if 'Chart.yaml' is present": {
			want: "helm",
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "Chart.yaml", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should return helm as a higher priority than kustomize": {
			want: "helm",
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "Chart.yaml", []byte{}, 0666)
				_ = billyUtils.WriteFile(memfs, "kustomization.yaml", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should return kustomize if 'kustomization.yaml' file is present": {
			want: AppTypeKustomize,
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "kustomization.yaml", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should return kustomize if 'kustomization.yml' file is present": {
			want: AppTypeKustomize,
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = billyUtils.WriteFile(memfs, "kustomization.yml", []byte{}, 0666)
				return fs.Create(memfs)
			},
		},
		"Should return kustomize if 'Kustomization' folder is present": {
			want: AppTypeKustomize,
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = memfs.MkdirAll("Kustomization", 0666)
				return fs.Create(memfs)
			},
		},
		"Should return dir if no other match": {
			want: AppTypeDirectory,
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				return fs.Create(memfs)
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			repofs := tt.beforeFn()
			if got := InferAppType(repofs); got != tt.want {
				t.Errorf("InferAppType() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeleteFromProject(t *testing.T) {
	tests := map[string]struct {
		wantErr  string
		beforeFn func() fs.FS
		assertFn func(*testing.T, fs.FS)
	}{
		"Should remove entire app folder, if it contains only one overlay": {
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = memfs.MkdirAll(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project"), 0666)
				return fs.Create(memfs)
			},
			assertFn: func(t *testing.T, repofs fs.FS) {
				assert.False(t, repofs.ExistsOrDie(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir)))
			},
		},
		"Should delete just the overlay, if there are more": {
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = memfs.MkdirAll(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project"), 0666)
				_ = memfs.MkdirAll(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project2"), 0666)
				return fs.Create(memfs)
			},
			assertFn: func(t *testing.T, repofs fs.FS) {
				assert.True(t, repofs.ExistsOrDie(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir)))
				assert.False(t, repofs.ExistsOrDie(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project")))
			},
		},
		"Should remove directory apps": {
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = memfs.MkdirAll(filepath.Join(store.Default.AppsDir, "app", "project"), 0666)
				return fs.Create(memfs)
			},
			assertFn: func(t *testing.T, repofs fs.FS) {
				assert.False(t, repofs.ExistsOrDie(filepath.Join(store.Default.AppsDir, "app")))
			},
		},
		"Should not delete anything, if kust app is not in project": {
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = memfs.MkdirAll(filepath.Join(store.Default.AppsDir, "app", store.Default.OverlaysDir, "project2"), 0666)
				return fs.Create(memfs)
			},
			assertFn: func(t *testing.T, repofs fs.FS) {
				assert.True(t, repofs.ExistsOrDie(filepath.Join(store.Default.AppsDir, "app")))
			},
		},
		"Should not delete anything, if dir app is not in project": {
			beforeFn: func() fs.FS {
				memfs := memfs.New()
				_ = memfs.MkdirAll(filepath.Join(store.Default.AppsDir, "app", "project2"), 0666)
				return fs.Create(memfs)
			},
			assertFn: func(t *testing.T, repofs fs.FS) {
				assert.True(t, repofs.ExistsOrDie(filepath.Join(store.Default.AppsDir, "app")))
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			repofs := tt.beforeFn()
			if err := DeleteFromProject(repofs, "app", "project"); err != nil {
				if tt.wantErr != "" {
					assert.EqualError(t, err, tt.wantErr)
				} else {
					t.Errorf("DeleteFromProject() error = %v", err)
				}

				return
			}

			if tt.assertFn != nil {
				tt.assertFn(t, repofs)
			}
		})
	}
}
