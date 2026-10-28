package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	agenthubv1alpha1 "github.com/agenthub/operator/api/v1alpha1"
)

func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(s)
	_ = agenthubv1alpha1.AddToScheme(s)
	return s
}

func newReconciler(objs ...client.Object) (*AgentHubReconciler, client.Client) {
	scheme := testScheme()
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&agenthubv1alpha1.AgentHub{}).
		Build()
	return &AgentHubReconciler{Client: cl, Scheme: scheme}, cl
}

func baseCR() *agenthubv1alpha1.AgentHub {
	return &agenthubv1alpha1.AgentHub{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "default"},
		Spec: agenthubv1alpha1.AgentHubSpec{
			Env:      "dev",
			Database: agenthubv1alpha1.DatabaseSpec{URL: "postgres://u:p@host:5432/db"},
			BootstrapAdmin: agenthubv1alpha1.BootstrapAdminSpec{
				Email:    "admin@example.com",
				Password: "s3cret",
			},
		},
	}
}

func wantCondition(cr *agenthubv1alpha1.AgentHub, condType string) (metav1.ConditionStatus, string) {
	for _, c := range cr.Status.Conditions {
		if c.Type == condType {
			return c.Status, c.Reason
		}
	}
	return "", ""
}

func TestReconcileValid(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	cr := baseCR()
	r, cl := newReconciler(cr)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	ctx := context.Background()
	var dep appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, &dep); err != nil {
		t.Fatalf("deployment should be created: %v", err)
	}
	var cm corev1.ConfigMap
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo-config", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("configmap should be created: %v", err)
	}
	if cm.Data[envEnv] != "dev" {
		t.Fatalf("configmap AGENTHUB_ENV = %q, want dev", cm.Data[envEnv])
	}
	var sec corev1.Secret
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo-secrets", Namespace: "default"}, &sec); err != nil {
		t.Fatalf("secret should be created: %v", err)
	}
	if string(sec.Data[envDatabaseURL]) != "postgres://u:p@host:5432/db" {
		t.Fatalf("secret DATABASE_URL = %q", string(sec.Data[envDatabaseURL]))
	}
	if string(sec.Data[envBootstrapPwd]) != "s3cret" {
		t.Fatalf("secret bootstrap password = %q", string(sec.Data[envBootstrapPwd]))
	}

	var got agenthubv1alpha1.AgentHub
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get AgentHub: %v", err)
	}
	if st, _ := wantCondition(&got, agenthubv1alpha1.ConditionAvailable); st == "" {
		t.Fatalf("expected an Available condition to be set, got none: %+v", got.Status.Conditions)
	}
}

func TestReconcileMissingDatabase(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	cr := baseCR()
	cr.Spec.Database = agenthubv1alpha1.DatabaseSpec{} // neither url nor existingSecret
	r, cl := newReconciler(cr)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	ctx := context.Background()
	var dep appsv1.Deployment
	err := cl.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, &dep)
	if err == nil {
		t.Fatalf("deployment must NOT be created when DATABASE_URL is missing")
	}

	var got agenthubv1alpha1.AgentHub
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get AgentHub: %v", err)
	}
	st, reason := wantCondition(&got, agenthubv1alpha1.ConditionAvailable)
	if st != metav1.ConditionFalse || reason != "MissingDatabase" {
		t.Fatalf("expected Available=False/MissingDatabase, got status=%q reason=%q", st, reason)
	}
}

func TestReconcileProdMissingEncryptionKey(t *testing.T) {
	ctrl.SetLogger(zap.New(zap.UseDevMode(true)))
	cr := baseCR()
	cr.Spec.Env = "prod"
	cr.Spec.EncryptionKey = agenthubv1alpha1.SecretOrValue{Value: "tooshort"} // < 32 bytes
	r, cl := newReconciler(cr)

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "demo", Namespace: "default"},
	}); err != nil {
		t.Fatalf("reconcile returned error: %v", err)
	}

	ctx := context.Background()
	var dep appsv1.Deployment
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, &dep); err == nil {
		t.Fatalf("deployment must NOT be created when prod encryption key is too weak")
	}

	var got agenthubv1alpha1.AgentHub
	if err := cl.Get(ctx, types.NamespacedName{Name: "demo", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get AgentHub: %v", err)
	}
	_, reason := wantCondition(&got, agenthubv1alpha1.ConditionAvailable)
	if reason != "WeakEncryptionKey" {
		t.Fatalf("expected Available reason WeakEncryptionKey, got %q", reason)
	}
}
