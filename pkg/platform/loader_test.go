package platform

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestLoaderInvalidate(t *testing.T) {
	ctx := context.Background()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Namespace: "project-system", Name: ConfigMapName},
		Data:       map[string]string{ConfigKey: "namespacePrefix: old-\n"},
	}
	c := fake.NewClientBuilder().WithObjects(cm).Build()
	l := &Loader{Reader: c, Namespace: "project-system"}

	prefix := func() string {
		t.Helper()
		cfg, err := l.Get(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return cfg.NamespacePrefix
	}
	if got := prefix(); got != "old-" {
		t.Fatalf("prefix = %q, want old-", got)
	}

	// The cache holds the old config until it is invalidated.
	update(t, c, "namespacePrefix: new-\n")
	if got := prefix(); got != "old-" {
		t.Fatalf("cached prefix = %q, want old- before invalidation", got)
	}

	l.Invalidate()
	if got := prefix(); got != "new-" {
		t.Fatalf("prefix after invalidation = %q, want new-", got)
	}
}

func update(t *testing.T, c client.Client, data string) {
	t.Helper()
	cm := &corev1.ConfigMap{}
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "project-system", Name: ConfigMapName}, cm); err != nil {
		t.Fatal(err)
	}
	cm.Data[ConfigKey] = data
	if err := c.Update(context.Background(), cm); err != nil {
		t.Fatal(err)
	}
}
