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

func int32p(v int32) *int32 { return &v }
func boolp(v bool) *bool    { return &v }

// Every knob added to the CR has to reach the process under the exact name the
// server reads. A typo here is invisible in production: the Pod starts with the
// default and the setting silently does nothing, which is worse than a refusal.
func TestBuildConfigRendersTheGovernanceKnobs(t *testing.T) {
	cr := baseCR()
	cr.Spec.RLS = RLSModeOff
	cr.Spec.RLSAck = boolp(true)
	cr.Spec.AgentTimeout = "90s"
	cr.Spec.A2ALegacyMethods = boolp(true)
	cr.Spec.MigrateDatabaseURL = &agenthubv1alpha1.DatabaseSpec{
		ExistingSecret: "agenthub-migrate", SecretKey: "MIGRATE_URL",
	}
	cr.Spec.Audit = &agenthubv1alpha1.AuditSpec{
		Enabled: boolp(true), Retention: "720h", Queue: int32p(1024), Batch: int32p(64),
	}
	cr.Spec.Sandbox = &agenthubv1alpha1.SandboxSpec{
		Enabled: boolp(true), Provider: "docker", DefaultImage: "alpine:3",
		IdleTTL: "10m", MaxTTL: "1h", SweepInterval: "30s",
		MaxPerOwner: int32p(2), MaxTotal: int32p(8),
		EgressAllowlist: []string{"a.example.test", "b.example.test"},
	}
	cr.Spec.Router = &agenthubv1alpha1.RouterSpec{
		Enabled: boolp(true), Interval: "15s", MaxTickets: int32p(4),
		AssessMode: "llm", AssessResource: "model-1", Resource: "agent-1",
	}

	cm, _, extra := buildConfig(cr)
	for key, want := range map[string]string{
		envRLS:            "off",
		envRLSAck:         "true",
		envAgentTimeout:   "90s",
		envA2ALegacy:      "true",
		envAuditEnabled:   "true",
		envAuditRetention: "720h",
		envAuditQueue:     "1024",
		envAuditBatch:     "64",
		envSandboxEnabled: "true",
		envSandboxProv:    "docker",
		envSandboxImage:   "alpine:3",
		envSandboxIdle:    "10m",
		envSandboxMaxTTL:  "1h",
		envSandboxSweep:   "30s",
		envSandboxPerOwn:  "2",
		envSandboxTotal:   "8",
		envSandboxEgress:  "a.example.test,b.example.test",
		envRouterEnabled:  "true",
		envRouterInterval: "15s",
		envRouterTickets:  "4",
		envRouterAssess:   "llm",
		envRouterAssessR:  "model-1",
		envRouterResource: "agent-1",
	} {
		if got := cm.Data[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}

	// A referenced Secret becomes a secretKeyRef, so the value never enters the
	// CR-managed objects at all.
	ref := lookupEnv(extra, envMigrateDBURL)
	if ref == nil || ref.ValueFrom == nil || ref.ValueFrom.SecretKeyRef == nil {
		t.Fatalf("%s must come from a secretKeyRef, got %+v", envMigrateDBURL, ref)
	}
	if ref.ValueFrom.SecretKeyRef.Name != "agenthub-migrate" || ref.ValueFrom.SecretKeyRef.Key != "MIGRATE_URL" {
		t.Errorf("secretKeyRef = %+v, want agenthub-migrate/MIGRATE_URL", ref.ValueFrom.SecretKeyRef)
	}
}

func lookupEnv(env []corev1.EnvVar, name string) *corev1.EnvVar {
	for i := range env {
		if env[i].Name == name {
			return &env[i]
		}
	}
	return nil
}

// The controller refuses the same combination the server refuses, one layer
// earlier: a CR that cannot produce a running Pod is a worse experience than a
// condition that says why.
func TestValidateRefusesRLSOffWithoutAcknowledgement(t *testing.T) {
	prod := baseCR()
	prod.Spec.Env = "prod"
	prod.Spec.EncryptionKey = agenthubv1alpha1.SecretOrValue{Value: "0123456789012345678901234567890123"}
	prod.Spec.RLS = RLSModeOff
	if reason, _ := validate(prod); reason != "RLSOffNotAcknowledged" {
		t.Fatalf("prod + rls=off = %q, want RLSOffNotAcknowledged", reason)
	}

	// Acknowledged: the same spec is accepted.
	prod.Spec.RLSAck = boolp(true)
	if reason, msg := validate(prod); reason != "" {
		t.Fatalf("acknowledged rls=off = %q (%s), want accepted", reason, msg)
	}

	// Outside production the acknowledgement is not demanded: it exists to stop
	// a staging value being carried into production, not to block a lab.
	dev := baseCR()
	dev.Spec.RLS = RLSModeOff
	if reason, msg := validate(dev); reason != "" {
		t.Fatalf("dev + rls=off = %q (%s), want accepted", reason, msg)
	}

	// An unknown mode is refused everywhere, rather than being passed to a Pod
	// that would exit at boot.
	bad := baseCR()
	bad.Spec.RLS = "maybe"
	if reason, _ := validate(bad); reason != "InvalidRLSMode" {
		t.Fatalf("rls=maybe = %q, want InvalidRLSMode", reason)
	}
}
