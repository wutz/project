/*
Copyright 2026.

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

package controller

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	forgeaiv1alpha1 "github.com/wutz/forgeai/api/v1alpha1"
	"github.com/wutz/forgeai/pkg/harbor"
	"github.com/wutz/forgeai/pkg/harbor/harbortest"
	"github.com/wutz/forgeai/pkg/platform"
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

const configNamespace = "forgeai-system"

const testPlatformConfig = `
namespacePrefix: forge-
apiServer: https://api.example.com:6443
ingress: {className: nginx, domain: example.com, port: 7443}
registry: {domain: cr.example.com:7443, adminSecret: harbor-admin}
storageTypes:
  shared: {storageClass: shared}
defaultStorageType: shared
instanceTypes:
  reserved: {cpu: "8", memory: 64Gi}
  sizes: [1, 2, 4, 8]
defaultQuota:
  gpus: {"*": 4}
  storage: 1Ti
network:
  systemNamespaces: [ingress-nginx]
sandbox:
  idleTimeout: 1h
training:
  stuckTimeout: 1s
trashRetention: 1h
`

var (
	ctx        context.Context
	cancel     context.CancelFunc
	testEnv    *envtest.Environment
	cfg        *rest.Config
	k8sClient  client.Client
	fakeHarbor *harbortest.Server
	activity   = &fakeActivity{}
)

// fakeActivity stands in for reading the dufs access log.
type fakeActivity struct {
	mu sync.Mutex
	t  time.Time
}

func (f *fakeActivity) get(context.Context, string, string, time.Time) (time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.t, nil
}

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	ctx, cancel = context.WithCancel(context.TODO())

	var err error
	err = forgeaiv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths: []string{
			filepath.Join("..", "..", "config", "crd", "bases"),
			// Trainer and Kueue CRDs exported from the dev cluster.
			filepath.Join("testdata", "crds"),
		},
		ErrorIfCRDPathMissing: true,
	}

	if getFirstFoundEnvTestBinaryDir() != "" {
		testEnv.BinaryAssetsDirectory = getFirstFoundEnvTestBinaryDir()
	}

	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	fakeHarbor = harbortest.New("admin", "harbor-pw")
	Expect(k8sClient.Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: configNamespace}})).To(Succeed())
	Expect(k8sClient.Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: platform.ConfigMapName, Namespace: configNamespace},
		Data:       map[string]string{platform.ConfigKey: testPlatformConfig},
	})).To(Succeed())
	Expect(k8sClient.Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "harbor-admin", Namespace: configNamespace},
		StringData: map[string]string{"username": "admin", "password": "harbor-pw"},
	})).To(Succeed())

	mgr, err := ctrl.NewManager(cfg, ctrl.Options{
		Scheme:  scheme.Scheme,
		Metrics: metricsserver.Options{BindAddress: "0"},
		Client:  client.Options{Cache: &client.CacheOptions{Unstructured: true}},
	})
	Expect(err).NotTo(HaveOccurred())
	loader := &platform.Loader{Reader: mgr.GetAPIReader(), Namespace: configNamespace}
	for _, r := range []interface{ SetupWithManager(ctrl.Manager) error }{
		&ProjectReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: mgr.GetScheme(), Config: loader},
		&RegistryReconciler{
			Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Config: loader, ConfigNamespace: configNamespace,
			NewHarbor: func(_ platform.Registry, u, p string) *harbor.Client { return harbor.New(fakeHarbor.URL, u, p, true) },
		},
		&InstanceTypeReconciler{Client: mgr.GetClient(), Config: loader},
		&FileServerReconciler{Client: mgr.GetClient(), Scheme: mgr.GetScheme(), Config: loader, Activity: activity.get},
		&PersistentVolumeClaimReconciler{Client: mgr.GetClient(), Config: loader},
		&TrainJobReconciler{Client: mgr.GetClient(), Config: loader, Recorder: mgr.GetEventRecorder("forgeai-test")},
		&SandboxReconciler{Client: mgr.GetClient(), Config: loader},
	} {
		Expect(r.SetupWithManager(mgr)).To(Succeed())
	}

	go func() {
		defer GinkgoRecover()
		Expect(mgr.Start(ctx)).To(Succeed())
	}()
})

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	cancel()
	fakeHarbor.Close()
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

// getFirstFoundEnvTestBinaryDir locates the first binary in the specified path.
// ENVTEST-based tests depend on specific binaries, usually located in paths set by
// controller-runtime. When running tests directly (e.g., via an IDE) without using
// Makefile targets, the 'BinaryAssetsDirectory' must be explicitly configured.
func getFirstFoundEnvTestBinaryDir() string {
	basePath := filepath.Join("..", "..", "bin", "k8s")
	entries, err := os.ReadDir(basePath)
	if err != nil {
		logf.Log.Error(err, "Failed to read directory", "path", basePath)
		return ""
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return filepath.Join(basePath, entry.Name())
		}
	}
	return ""
}
