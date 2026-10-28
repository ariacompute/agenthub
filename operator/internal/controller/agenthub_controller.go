package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/log"

	agenthubv1alpha1 "github.com/agenthub/operator/api/v1alpha1"
)

const (
	finalizerName = "agenthub.ai/agenthub"

	labelName      = "app.kubernetes.io/name"
	labelInstance  = "app.kubernetes.io/instance"
	labelManagedBy = "app.kubernetes.io/managed-by"
	labelCR        = "agenthub.ai/agent-hub"

	managedByValue = "agenthub-operator"
	appNameValue   = "agenthub"

	containerPortName = "http"
	healthPath        = "/healthz"
	backupMountPath   = "/var/lib/agenthub/backups"

	envHTTPPort        = "AGENTHUB_HTTP_PORT"
	envEnv             = "AGENTHUB_ENV"
	envStaticDir       = "AGENTHUB_STATIC_DIR"
	envBootstrapEmail  = "AGENTHUB_BOOTSTRAP_ADMIN_EMAIL"
	envBootstrapName   = "AGENTHUB_BOOTSTRAP_ADMIN_NAME"
	envSessionTTL      = "AGENTHUB_SESSION_TTL"
	envCookieSecure    = "AGENTHUB_COOKIE_SECURE"
	envPublicBaseURL   = "AGENTHUB_PUBLIC_BASE_URL"
	envDatabaseURL     = "DATABASE_URL"
	envSeedDemo        = "AGENTHUB_SEED_DEMO"
	envMaxParallel     = "AGENTHUB_MAX_PARALLEL"
	envCLITimeout      = "AGENTHUB_CLI_TIMEOUT"
	envCLIAllowlist    = "AGENTHUB_CLI_ALLOWLIST"
	envMaxOutputBytes  = "AGENTHUB_MAX_OUTPUT_BYTES"
	envEventBuffer     = "AGENTHUB_EVENT_BUFFER"
	envPushEnabled     = "AGENTHUB_PUSH_ENABLED"
	envPushRecordLimit = "AGENTHUB_PUSH_RECORD_LIMIT"
	envEncryptionKey   = "AGENTHUB_ENCRYPTION_KEY"
	envPushSecret      = "AGENTHUB_PUSH_SECRET"
	envBackupDir       = "AGENTHUB_BACKUP_DIR"
	envBackupMode      = "AGENTHUB_BACKUP_MODE"
	envBackupInterval  = "AGENTHUB_BACKUP_INTERVAL"
	envBackupDaily     = "AGENTHUB_BACKUP_DAILY_TIME"
	envBackupTZ        = "AGENTHUB_BACKUP_TIMEZONE"
	envBackupCron      = "AGENTHUB_BACKUP_CRON"
	envBackupKeepLast  = "AGENTHUB_BACKUP_KEEP_LAST"
	envBackupMaxAge    = "AGENTHUB_BACKUP_MAX_AGE_DAYS"
	envRegSyncEnabled  = "AGENTHUB_REGISTRY_SYNC_ENABLED"
	envRegSyncInt      = "AGENTHUB_REGISTRY_SYNC_INTERVAL"
	envRegSyncFixture  = "AGENTHUB_REGISTRY_SYNC_FIXTURE"
	envModelRouter     = "AGENTHUB_MODEL_ROUTER_MODE"
	envChannels        = "AGENTHUB_CHANNELS_ENABLED"
	envSpeech          = "AGENTHUB_SPEECH_MODE"
	envAudioMaxBytes   = "AGENTHUB_AUDIO_MAX_BYTES"
	envOAuthEnabled    = "AGENTHUB_OAUTH_ENABLED"
	envOAuthGoogleID   = "AGENTHUB_OAUTH_GOOGLE_CLIENT_ID"
	envOAuthGoogleSec  = "AGENTHUB_OAUTH_GOOGLE_CLIENT_SECRET"
	envOAuthGoogleSc   = "AGENTHUB_OAUTH_GOOGLE_SCOPES"
	envOAuthGitHubID   = "AGENTHUB_OAUTH_GITHUB_CLIENT_ID"
	envOAuthGitHubSec  = "AGENTHUB_OAUTH_GITHUB_CLIENT_SECRET"
	envOAuthGitHubSc   = "AGENTHUB_OAUTH_GITHUB_SCOPES"
	envOAuthOIDCiss    = "AGENTHUB_OAUTH_OIDC_ISSUER"
	envOAuthOIDCID     = "AGENTHUB_OAUTH_OIDC_CLIENT_ID"
	envOAuthOIDCSec    = "AGENTHUB_OAUTH_OIDC_CLIENT_SECRET"
	envOAuthOIDCSc     = "AGENTHUB_OAUTH_OIDC_SCOPES"
	envBootstrapPwd    = "AGENTHUB_BOOTSTRAP_ADMIN_PASSWORD"

	defaultHTTPPort = int32(3500)
	defaultEnv      = "dev"
	defaultStatic   = "/app/backend/static"
)

// AgentHubReconciler reconciles an AgentHub object into a running
// agenthub-server deployment plus its supporting resources.
type AgentHubReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=agenthub.ai,resources=agenthubs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=agenthub.ai,resources=agenthubs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=agenthub.ai,resources=agenthubs/finalizers,verbs=update
// +kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=configmaps,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=core,resources=serviceaccounts,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=rbac.authorization.k8s.io,resources=roles;rolebindings,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

// Reconcile is the controller-runtime entry point.
func (r *AgentHubReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	var cr agenthubv1alpha1.AgentHub
	if err := r.Get(ctx, req.NamespacedName, &cr); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	// Refresh the observed generation; the deferred status update runs even
	// on early returns.
	defer func() {
		cr.Status.ObservedGeneration = cr.Generation
		if err := r.Status().Update(ctx, &cr); err != nil {
			logger.Error(err, "failed to update AgentHub status")
		}
	}()

	// Deletion: drop the finalizer and let owner references cascade-delete.
	if !cr.DeletionTimestamp.IsZero() {
		if controllerutil.ContainsFinalizer(&cr, finalizerName) {
			controllerutil.RemoveFinalizer(&cr, finalizerName)
			if err := r.Update(ctx, &cr); err != nil {
				return ctrl.Result{}, err
			}
		}
		return ctrl.Result{}, nil
	}
	if !controllerutil.ContainsFinalizer(&cr, finalizerName) {
		controllerutil.AddFinalizer(&cr, finalizerName)
		if err := r.Update(ctx, &cr); err != nil {
			return ctrl.Result{}, err
		}
	}

	// Validate early: a broken config would make the Pod crash-loop, so we
	// refuse to create the workload and surface the reason via status.
	if reason, msg := validate(&cr); reason != "" {
		logger.Info("AgentHub config invalid, pausing reconcile", "reason", reason, "message", msg)
		setCondition(&cr, agenthubv1alpha1.ConditionAvailable, metav1.ConditionFalse, reason, msg)
		setCondition(&cr, agenthubv1alpha1.ConditionProgressing, metav1.ConditionFalse, reason, msg)
		cr.Status.ReadyReplicas = 0
		return ctrl.Result{}, nil
	}

	labels := childLabels(&cr)
	selector := selectorLabels(&cr)

	cm, secret, extraEnv := buildConfig(&cr)
	cm.Name, cm.Namespace, cm.Labels = configMapName(&cr), cr.Namespace, labels
	secret.Name, secret.Namespace, secret.Labels = secretName(&cr), cr.Namespace, labels

	sa := buildServiceAccount(&cr, labels, selector)
	role := buildRole(&cr, labels, selector)
	rb := buildRoleBinding(&cr, labels, selector)
	svc := buildService(&cr, labels, selector)
	dep := buildDeployment(&cr, labels, selector, secret.Name, cm.Name, extraEnv)
	var pvc *corev1.PersistentVolumeClaim
	if persistenceEnabled(&cr) {
		p := buildPVC(&cr, labels, selector)
		pvc = &p
	}

	for _, obj := range []client.Object{&cm, &secret, &sa, &role, &rb, &svc} {
		if err := r.upsert(ctx, &cr, obj); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile %T: %w", obj, err)
		}
	}
	if pvc != nil {
		if err := r.upsert(ctx, &cr, pvc); err != nil {
			return ctrl.Result{}, fmt.Errorf("reconcile pvc: %w", err)
		}
	}
	if err := r.upsert(ctx, &cr, &dep); err != nil {
		return ctrl.Result{}, fmt.Errorf("reconcile deployment: %w", err)
	}

	var liveDep appsv1.Deployment
	if err := r.Get(ctx, types.NamespacedName{Name: dep.Name, Namespace: dep.Namespace}, &liveDep); err == nil {
		cr.Status.ReadyReplicas = liveDep.Status.ReadyReplicas
		desired := int32(1)
		if cr.Spec.Replicas != nil {
			desired = *cr.Spec.Replicas
		}
		if desired > 0 && liveDep.Status.ReadyReplicas >= desired {
			setCondition(&cr, agenthubv1alpha1.ConditionAvailable, metav1.ConditionTrue, "DeploymentReady", "agenthub is serving")
			setCondition(&cr, agenthubv1alpha1.ConditionProgressing, metav1.ConditionFalse, "DeploymentReady", "agenthub is serving")
		} else {
			setCondition(&cr, agenthubv1alpha1.ConditionAvailable, metav1.ConditionFalse, "DeploymentNotReady", "waiting for agenthub pods to become ready")
			setCondition(&cr, agenthubv1alpha1.ConditionProgressing, metav1.ConditionTrue, "DeploymentProgressing", "waiting for agenthub pods to become ready")
		}
	}

	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the manager.
func (r *AgentHubReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&agenthubv1alpha1.AgentHub{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&corev1.PersistentVolumeClaim{}).
		Owns(&rbacv1.Role{}).
		Owns(&rbacv1.RoleBinding{}).
		Complete(r)
}

// upsert creates or updates an owned object, preserving the resourceVersion
// on updates so the controller reference stays intact.
func (r *AgentHubReconciler) upsert(ctx context.Context, owner *agenthubv1alpha1.AgentHub, desired client.Object) error {
	if err := controllerutil.SetControllerReference(owner, desired, r.Scheme); err != nil {
		return err
	}
	existing := desired.DeepCopyObject().(client.Object)
	err := r.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return r.Create(ctx, desired)
	}
	if err != nil {
		return err
	}
	desired.SetResourceVersion(existing.GetResourceVersion())
	return r.Update(ctx, desired)
}

func validate(cr *agenthubv1alpha1.AgentHub) (string, string) {
	spec := cr.Spec
	if spec.Database.URL == "" && spec.Database.ExistingSecret == "" {
		return "MissingDatabase", "spec.database.url or spec.database.existingSecret is required (agenthub needs an external PostgreSQL)"
	}
	if spec.Env == "prod" {
		if strings.TrimSpace(spec.BootstrapAdmin.Email) == "" {
			return "MissingAdminEmail", "AGENTHUB_ENV=prod requires spec.bootstrapAdmin.email"
		}
		if spec.EncryptionKey.ExistingSecret == "" && len(spec.EncryptionKey.Value) < 32 {
			return "WeakEncryptionKey", "AGENTHUB_ENV=prod requires spec.encryptionKey with at least 32 bytes (or an existingSecret reference)"
		}
	}
	return "", ""
}

func setCondition(cr *agenthubv1alpha1.AgentHub, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i := range cr.Status.Conditions {
		if cr.Status.Conditions[i].Type == condType {
			if cr.Status.Conditions[i].Status != status {
				cr.Status.Conditions[i].LastTransitionTime = now
			}
			cr.Status.Conditions[i].Status = status
			cr.Status.Conditions[i].Reason = reason
			cr.Status.Conditions[i].Message = message
			cr.Status.Conditions[i].ObservedGeneration = cr.Generation
			return
		}
	}
	cr.Status.Conditions = append(cr.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cr.Generation,
		LastTransitionTime: now,
	})
}

func childLabels(cr *agenthubv1alpha1.AgentHub) map[string]string {
	return map[string]string{
		labelName:      appNameValue,
		labelInstance:  cr.Name,
		labelManagedBy: managedByValue,
		labelCR:        cr.Name,
	}
}

func selectorLabels(cr *agenthubv1alpha1.AgentHub) map[string]string {
	return map[string]string{
		labelName: appNameValue,
		labelCR:   cr.Name,
	}
}

func configMapName(cr *agenthubv1alpha1.AgentHub) string      { return cr.Name + "-config" }
func secretName(cr *agenthubv1alpha1.AgentHub) string         { return cr.Name + "-secrets" }
func serviceAccountName(cr *agenthubv1alpha1.AgentHub) string { return cr.Name + "-sa" }
func roleName(cr *agenthubv1alpha1.AgentHub) string           { return cr.Name + "-role" }
func pvcName(cr *agenthubv1alpha1.AgentHub) string            { return cr.Name + "-backups" }

func defaultString(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

func boolStr(b *bool) string {
	if b == nil {
		return ""
	}
	if *b {
		return "true"
	}
	return "false"
}

func persistenceEnabled(cr *agenthubv1alpha1.AgentHub) bool {
	return cr.Spec.Persistence != nil && (cr.Spec.Persistence.Enabled == nil || *cr.Spec.Persistence.Enabled)
}

// addSecretValue copies an inline SecretOrValue into the managed secret, or
// emits a secretKeyRef EnvVar when an existing Secret is referenced.
func addSecretValue(name string, sv agenthubv1alpha1.SecretOrValue, secret *corev1.Secret, extra *[]corev1.EnvVar) {
	if sv.ExistingSecret != "" {
		key := defaultString(sv.Key, name)
		*extra = append(*extra, corev1.EnvVar{
			Name: name,
			ValueFrom: &corev1.EnvVarSource{
				SecretKeyRef: &corev1.SecretKeySelector{
					LocalObjectReference: corev1.LocalObjectReference{Name: sv.ExistingSecret},
					Key:                  key,
				},
			},
		})
		return
	}
	if sv.Value != "" {
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		secret.Data[name] = []byte(sv.Value)
	}
}

// buildConfig renders the non-secret ConfigMap, the operator-managed Secret
// (inline secret values only) and the extra EnvVars that point straight at
// externally referenced Secrets.
func buildConfig(cr *agenthubv1alpha1.AgentHub) (corev1.ConfigMap, corev1.Secret, []corev1.EnvVar) {
	spec := cr.Spec
	cm := corev1.ConfigMap{Data: map[string]string{}}
	secret := corev1.Secret{Type: corev1.SecretTypeOpaque, Data: map[string][]byte{}}
	var extra []corev1.EnvVar

	putStr := func(k, v string) {
		if v != "" {
			cm.Data[k] = v
		}
	}

	httpPort := spec.HTTPPort
	if httpPort == 0 {
		httpPort = defaultHTTPPort
	}
	env := defaultString(spec.Env, defaultEnv)
	static := defaultString(spec.StaticDir, defaultStatic)

	putStr(envHTTPPort, fmt.Sprint(httpPort))
	putStr(envEnv, env)
	putStr(envStaticDir, static)
	putStr(envBootstrapEmail, spec.BootstrapAdmin.Email)
	putStr(envBootstrapName, spec.BootstrapAdmin.Name)
	putStr(envSessionTTL, spec.SessionTTL)
	putStr(envCookieSecure, boolStr(spec.CookieSecure))
	putStr(envPublicBaseURL, spec.PublicBaseURL)
	putStr(envSeedDemo, boolStr(spec.SeedDemo))
	if spec.MaxParallel != nil {
		putStr(envMaxParallel, fmt.Sprint(*spec.MaxParallel))
	}
	putStr(envCLITimeout, spec.CLITimeout)
	if len(spec.CLIAllowlist) > 0 {
		putStr(envCLIAllowlist, strings.Join(spec.CLIAllowlist, ","))
	}
	if spec.MaxOutputBytes != nil {
		putStr(envMaxOutputBytes, fmt.Sprint(*spec.MaxOutputBytes))
	}
	if spec.EventBuffer != nil {
		putStr(envEventBuffer, fmt.Sprint(*spec.EventBuffer))
	}
	if spec.PushEnabled != nil {
		putStr(envPushEnabled, fmt.Sprint(*spec.PushEnabled))
	}
	if spec.PushRecordLimit != nil {
		putStr(envPushRecordLimit, fmt.Sprint(*spec.PushRecordLimit))
	}
	putStr(envModelRouter, spec.ModelRouterMode)
	if spec.ChannelsEnabled != nil {
		putStr(envChannels, fmt.Sprint(*spec.ChannelsEnabled))
	}
	putStr(envSpeech, spec.SpeechMode)
	if spec.AudioMaxBytes != nil {
		putStr(envAudioMaxBytes, fmt.Sprint(*spec.AudioMaxBytes))
	}

	if spec.OAuth != nil {
		putStr(envOAuthEnabled, boolStr(spec.OAuth.Enabled))
		putStr(envOAuthGoogleID, spec.OAuth.Google.ClientID)
		putStr(envOAuthGoogleSc, strings.Join(spec.OAuth.Google.Scopes, ","))
		putStr(envOAuthGitHubID, spec.OAuth.GitHub.ClientID)
		putStr(envOAuthGitHubSc, strings.Join(spec.OAuth.GitHub.Scopes, ","))
		putStr(envOAuthOIDCiss, spec.OAuth.OIDC.Issuer)
		putStr(envOAuthOIDCID, spec.OAuth.OIDC.ClientID)
		putStr(envOAuthOIDCSc, strings.Join(spec.OAuth.OIDC.Scopes, ","))
		addSecretValue(envOAuthGoogleSec, agenthubv1alpha1.SecretOrValue{Value: spec.OAuth.Google.ClientSecret}, &secret, &extra)
		addSecretValue(envOAuthGitHubSec, agenthubv1alpha1.SecretOrValue{Value: spec.OAuth.GitHub.ClientSecret}, &secret, &extra)
		addSecretValue(envOAuthOIDCSec, agenthubv1alpha1.SecretOrValue{Value: spec.OAuth.OIDC.ClientSecret}, &secret, &extra)
	}

	addSecretValue(envDatabaseURL, agenthubv1alpha1.SecretOrValue{
		Value:          spec.Database.URL,
		ExistingSecret: spec.Database.ExistingSecret,
		Key:            defaultString(spec.Database.SecretKey, envDatabaseURL),
	}, &secret, &extra)
	addSecretValue(envBootstrapPwd, agenthubv1alpha1.SecretOrValue{
		Value:          spec.BootstrapAdmin.Password,
		ExistingSecret: spec.BootstrapAdmin.ExistingSecret,
		Key:            defaultString(spec.BootstrapAdmin.SecretKey, envBootstrapPwd),
	}, &secret, &extra)
	addSecretValue(envEncryptionKey, spec.EncryptionKey, &secret, &extra)
	addSecretValue(envPushSecret, spec.PushSecret, &secret, &extra)

	if persistenceEnabled(cr) {
		putStr(envBackupDir, backupMountPath)
		if spec.Backup != nil {
			putStr(envBackupMode, spec.Backup.Mode)
			putStr(envBackupInterval, spec.Backup.Interval)
			putStr(envBackupDaily, spec.Backup.DailyTime)
			putStr(envBackupTZ, spec.Backup.Timezone)
			putStr(envBackupCron, spec.Backup.Cron)
			if spec.Backup.KeepLast != 0 {
				putStr(envBackupKeepLast, fmt.Sprint(spec.Backup.KeepLast))
			}
			if spec.Backup.MaxAgeDays != 0 {
				putStr(envBackupMaxAge, fmt.Sprint(spec.Backup.MaxAgeDays))
			}
		}
	}

	if spec.RegistrySync != nil {
		putStr(envRegSyncEnabled, boolStr(spec.RegistrySync.Enabled))
		putStr(envRegSyncInt, spec.RegistrySync.Interval)
		putStr(envRegSyncFixture, boolStr(spec.RegistrySync.Fixture))
	}

	return cm, secret, extra
}

func buildServiceAccount(cr *agenthubv1alpha1.AgentHub, labels, _ map[string]string) corev1.ServiceAccount {
	return corev1.ServiceAccount{
		ObjectMeta: metav1.ObjectMeta{
			Name:      serviceAccountName(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
	}
}

func buildRole(cr *agenthubv1alpha1.AgentHub, labels, _ map[string]string) rbacv1.Role {
	return rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{""},
				Resources: []string{"events"},
				Verbs:     []string{"create", "patch"},
			},
		},
	}
}

func buildRoleBinding(cr *agenthubv1alpha1.AgentHub, labels, _ map[string]string) rbacv1.RoleBinding {
	return rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name:      roleName(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: rbacv1.GroupName,
			Kind:     "Role",
			Name:     roleName(cr),
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      serviceAccountName(cr),
				Namespace: cr.Namespace,
			},
		},
	}
}

func buildService(cr *agenthubv1alpha1.AgentHub, labels, selector map[string]string) corev1.Service {
	httpPort := cr.Spec.HTTPPort
	if httpPort == 0 {
		httpPort = defaultHTTPPort
	}
	return corev1.Service{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.ServiceSpec{
			Type:     corev1.ServiceTypeClusterIP,
			Selector: selector,
			Ports: []corev1.ServicePort{
				{
					Name:       containerPortName,
					Port:       httpPort,
					TargetPort: intstr.FromString(containerPortName),
					Protocol:   corev1.ProtocolTCP,
				},
			},
		},
	}
}

func buildPVC(cr *agenthubv1alpha1.AgentHub, labels, _ map[string]string) corev1.PersistentVolumeClaim {
	size := "10Gi"
	if cr.Spec.Persistence != nil && cr.Spec.Persistence.Size != "" {
		size = cr.Spec.Persistence.Size
	}
	pvc := corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Name:      pvcName(cr),
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceStorage: resource.MustParse(size),
				},
			},
		},
	}
	if cr.Spec.Persistence != nil && cr.Spec.Persistence.StorageClass != nil {
		pvc.Spec.StorageClassName = cr.Spec.Persistence.StorageClass
	}
	return pvc
}

func buildDeployment(cr *agenthubv1alpha1.AgentHub, labels, selector map[string]string, secretName, cmName string, extraEnv []corev1.EnvVar) appsv1.Deployment {
	spec := cr.Spec
	httpPort := spec.HTTPPort
	if httpPort == 0 {
		httpPort = defaultHTTPPort
	}
	replicas := int32(1)
	if spec.Replicas != nil {
		replicas = *spec.Replicas
	}

	image := agenthubv1alpha1.ImageSpec{Repository: "agenthub", Tag: "latest", PullPolicy: corev1.PullIfNotPresent}
	if spec.Image.Repository != "" {
		image.Repository = spec.Image.Repository
	}
	if spec.Image.Tag != "" {
		image.Tag = spec.Image.Tag
	}
	if spec.Image.PullPolicy != "" {
		image.PullPolicy = spec.Image.PullPolicy
	}

	container := corev1.Container{
		Name:  "agenthub",
		Image: fmt.Sprintf("%s:%s", image.Repository, image.Tag),
		Args:  []string{},
		Ports: []corev1.ContainerPort{
			{Name: containerPortName, ContainerPort: httpPort, Protocol: corev1.ProtocolTCP},
		},
		EnvFrom: []corev1.EnvFromSource{
			{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: cmName}}},
			{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}}},
		},
		Env: extraEnv,
		LivenessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: healthPath, Port: intstr.FromInt32(httpPort)},
			},
			InitialDelaySeconds: 20,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    5,
		},
		ReadinessProbe: &corev1.Probe{
			ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Path: healthPath, Port: intstr.FromInt32(httpPort)},
			},
			InitialDelaySeconds: 20,
			PeriodSeconds:       10,
			TimeoutSeconds:      5,
			FailureThreshold:    5,
		},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: boolPtr(false),
			RunAsNonRoot:             boolPtr(true),
			SeccompProfile:           &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
		},
	}
	if spec.Resources != nil {
		container.Resources = *spec.Resources
	}

	var volumes []corev1.Volume
	if persistenceEnabled(cr) {
		volumes = append(volumes, corev1.Volume{
			Name: "backups",
			VolumeSource: corev1.VolumeSource{
				PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName(cr)},
			},
		})
		container.VolumeMounts = append(container.VolumeMounts, corev1.VolumeMount{
			Name:      "backups",
			MountPath: backupMountPath,
		})
	}

	return appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      cr.Name,
			Namespace: cr.Namespace,
			Labels:    labels,
		},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: selector},
			Strategy: appsv1.DeploymentStrategy{
				Type: appsv1.RollingUpdateDeploymentStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDeployment{
					MaxUnavailable: intStrPtr(0),
				},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: mergeLabels(labels, selector)},
				Spec: corev1.PodSpec{
					ServiceAccountName: serviceAccountName(cr),
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   boolPtr(true),
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{container},
					Volumes:    volumes,
				},
			},
		},
	}
}

func boolPtr(b bool) *bool { return &b }

func intStrPtr(i int) *intstr.IntOrString {
	v := intstr.FromInt32(int32(i))
	return &v
}

func mergeLabels(a, b map[string]string) map[string]string {
	out := make(map[string]string, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
