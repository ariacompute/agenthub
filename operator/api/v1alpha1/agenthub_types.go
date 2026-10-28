package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AgentHub is the Schema for the agenthubs API. A single AgentHub CR fully
// describes one agenthub-server deployment: its image, replica count, every
// AGENTHUB_* knob and the external PostgreSQL connection (or a reference to a
// Secret that holds it). The controller reconciles the CR into a
// Deployment/Service/ConfigMap/Secret/ServiceAccount/Role set.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=ahub,scope=Namespaced
// +kubebuilder:printcolumn:name="Env",type=string,JSONPath=`.spec.env`
// +kubebuilder:printcolumn:name="Ready",type=integer,JSONPath=`.status.readyReplicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type AgentHub struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AgentHubSpec   `json:"spec,omitempty"`
	Status AgentHubStatus `json:"status,omitempty"`
}

// AgentHubList contains a list of AgentHub.
//
// +kubebuilder:object:root=true
type AgentHubList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AgentHub `json:"items"`
}

// SecretOrValue carries a sensitive value either inline (Value) or by
// referencing a key in an existing Secret (ExistingSecret/Key). Inline values
// are copied into the operator-managed Secret; referenced ones are injected
// straight from the external Secret via secretKeyRef.
type SecretOrValue struct {
	Value          string `json:"value,omitempty"`
	ExistingSecret string `json:"existingSecret,omitempty"`
	Key            string `json:"key,omitempty"`
}

// ImageSpec describes the agenthub-server container image.
type ImageSpec struct {
	Repository string            `json:"repository,omitempty"`
	Tag        string            `json:"tag,omitempty"`
	PullPolicy corev1.PullPolicy `json:"pullPolicy,omitempty"`
}

// DatabaseSpec is the external PostgreSQL connection. Either URL (plaintext,
// example only) or ExistingSecret + SecretKey (production) must be provided.
type DatabaseSpec struct {
	URL            string `json:"url,omitempty"`
	ExistingSecret string `json:"existingSecret,omitempty"`
	SecretKey      string `json:"secretKey,omitempty"`
}

// BootstrapAdminSpec seeds the first administrator account.
type BootstrapAdminSpec struct {
	Email          string `json:"email,omitempty"`
	Name           string `json:"name,omitempty"`
	Password       string `json:"password,omitempty"`
	ExistingSecret string `json:"existingSecret,omitempty"`
	SecretKey      string `json:"secretKey,omitempty"`
}

// OAuthProviderSpec holds one OAuth identity provider's credentials.
type OAuthProviderSpec struct {
	Issuer       string   `json:"issuer,omitempty"`
	ClientID     string   `json:"clientID,omitempty"`
	ClientSecret string   `json:"clientSecret,omitempty"`
	Scopes       []string `json:"scopes,omitempty"`
}

// OAuthSpec is the OAuth provider configuration.
type OAuthSpec struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Google  OAuthProviderSpec `json:"google,omitempty"`
	GitHub  OAuthProviderSpec `json:"github,omitempty"`
	OIDC    OAuthProviderSpec `json:"oidc,omitempty"`
}

// BackupSpec sets the initial database checkpoint policy. Checkpointing is
// only switched on when Persistence is enabled (which mounts the backup dir).
type BackupSpec struct {
	Mode       string `json:"mode,omitempty"`
	Interval   string `json:"interval,omitempty"`
	DailyTime  string `json:"dailyTime,omitempty"`
	Timezone   string `json:"timezone,omitempty"`
	Cron       string `json:"cron,omitempty"`
	KeepLast   int    `json:"keepLast,omitempty"`
	MaxAgeDays int    `json:"maxAgeDays,omitempty"`
}

// RegistrySyncSpec configures the upstream registry background puller.
type RegistrySyncSpec struct {
	Enabled  *bool  `json:"enabled,omitempty"`
	Interval string `json:"interval,omitempty"`
	Fixture  *bool  `json:"fixture,omitempty"`
}

// PersistenceSpec mounts a PVC for database checkpoints at
// /var/lib/agenthub/backups and turns on the backup feature.
type PersistenceSpec struct {
	Enabled      *bool   `json:"enabled,omitempty"`
	Size         string  `json:"size,omitempty"`
	StorageClass *string `json:"storageClass,omitempty"`
}

// AgentHubSpec is the desired state of an agenthub deployment.
type AgentHubSpec struct {
	Image           ImageSpec                    `json:"image,omitempty"`
	Replicas        *int32                       `json:"replicas,omitempty"`
	Env             string                       `json:"env,omitempty"`
	HTTPPort        int32                        `json:"httpPort,omitempty"`
	StaticDir       string                       `json:"staticDir,omitempty"`
	BootstrapAdmin  BootstrapAdminSpec           `json:"bootstrapAdmin,omitempty"`
	SessionTTL      string                       `json:"sessionTTL,omitempty"`
	CookieSecure    *bool                        `json:"cookieSecure,omitempty"`
	PublicBaseURL   string                       `json:"publicBaseURL,omitempty"`
	Database        DatabaseSpec                 `json:"database,omitempty"`
	OAuth           *OAuthSpec                   `json:"oauth,omitempty"`
	SeedDemo        *bool                        `json:"seedDemo,omitempty"`
	MaxParallel     *int32                       `json:"maxParallel,omitempty"`
	CLITimeout      string                       `json:"cliTimeout,omitempty"`
	CLIAllowlist    []string                     `json:"cliAllowlist,omitempty"`
	MaxOutputBytes  *int32                       `json:"maxOutputBytes,omitempty"`
	EventBuffer     *int32                       `json:"eventBuffer,omitempty"`
	PushEnabled     *bool                        `json:"pushEnabled,omitempty"`
	PushRecordLimit *int32                       `json:"pushRecordLimit,omitempty"`
	EncryptionKey   SecretOrValue                `json:"encryptionKey,omitempty"`
	PushSecret      SecretOrValue                `json:"pushSecret,omitempty"`
	Backup          *BackupSpec                  `json:"backup,omitempty"`
	RegistrySync    *RegistrySyncSpec            `json:"registrySync,omitempty"`
	ModelRouterMode string                       `json:"modelRouterMode,omitempty"`
	ChannelsEnabled *bool                        `json:"channelsEnabled,omitempty"`
	SpeechMode      string                       `json:"speechMode,omitempty"`
	AudioMaxBytes   *int32                       `json:"audioMaxBytes,omitempty"`
	Resources       *corev1.ResourceRequirements `json:"resources,omitempty"`
	Persistence     *PersistenceSpec             `json:"persistence,omitempty"`
}

// AgentHubStatus is the observed state, patched by the controller.
type AgentHubStatus struct {
	ObservedGeneration int64              `json:"observedGeneration,omitempty"`
	Conditions         []metav1.Condition `json:"conditions,omitempty"`
	ReadyReplicas      int32              `json:"readyReplicas,omitempty"`
}

// Condition types reported on AgentHub.Status.Conditions.
const (
	// ConditionAvailable is True when the agenthub deployment is fully
	// reconciled and serving; False when a blocking misconfiguration (e.g.
	// missing DATABASE_URL or a prod missing its encryption key) prevents it.
	ConditionAvailable = "Available"

	// ConditionProgressing is True while the deployment is being created or
	// scaled and not yet at the desired replica count.
	ConditionProgressing = "Progressing"
)
