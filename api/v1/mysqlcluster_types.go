package v1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type StorageConfig struct {
	// +kubebuilder:validation:Optional
	// 使用指针以区分未设置和空字符串，如果是nil会使用默认StorageClass
	StorageClassName *string `json:"storageClassName,omitempty"`

	// +kubebuilder:validation:Required
	// 使用官方提供的resource.Quantity类型来表示存储大小，cpu和内存也是一样的
	Size resource.Quantity `json:"size"`
}

type MysqlClusterSpec struct {

	// +kubebuilder:validation:Required
	Image string `json:"image"`

	// +kubebuilder:validation:Minimum=2
	// +kubebuilder:default=3
	// +kubebuilder:validation:Optional
	// 使用指针区分用户未设置和设置为0的情况，当然我们没有缩容所以没用。建议设置最小值和默认值
	// 避免使用无符号数，原因是兼容性和溢出
	Replicas *int32 `json:"replicas,omitempty"`

	// +kubebuilder:validation:Required
	Storage StorageConfig `json:"storage"`

	// +kubebuilder:validation:Required
	// 使用官方提供的 ResourceRequirements 类型来表示资源请求和限制
	Resources corev1.ResourceRequirements `json:"resources"`

	// +kubebuilder:validation:Required
	// 强制要求用户自己创建一个secret，并有2个key，一个root-password，一个repl-password用于主从同步，否则直接报错
	SecretName corev1.LocalObjectReference `json:"secretName"`
}

// +kubebuilder:validation:Enum=Pending;Initializing;Running;Failed;Degraded;Terminating
type MysqlClusterPhase string

const (
	MysqlClusterPhaseTerminating  MysqlClusterPhase = "Terminating"  // 正在删除
	MysqlClusterPhasePending      MysqlClusterPhase = "Pending"      // pod启动了就进入pending
	MysqlClusterPhaseDegraded     MysqlClusterPhase = "Degraded"     // 有一主一丛正常就是degraded
	MysqlClusterPhaseInitializing MysqlClusterPhase = "Initializing" // 刚创建，正在初始化
	MysqlClusterPhaseRunning      MysqlClusterPhase = "Running"      // 一主多从都正常
	MysqlClusterPhaseFailed       MysqlClusterPhase = "Failed"       // 主库不可用
)

// 单个pod的状态
type PodStatus struct {
	Name          string `json:"name"` // 只存pod名字，快照中会放pod对象
	Role          string `json:"role"`
	IsReady       bool   `json:"isReady"`       // k8s层面ready
	IsConnectable bool   `json:"IsConnectable"` // 数据库层面可连接

	// 不建议放gtid，频繁变动会导致更多的网络io
}
type MysqlClusterStatus struct {

	// 表示当前集群的状态
	Phase MysqlClusterPhase `json:"phase,omitempty"`

	// 这些数据用于给kubectl get显示信息
	MasterReplicas int32  `json:"masterReplicas"`
	SlaveReplicas  int32  `json:"slaveReplicas"`
	MasterDisplay  string `json:"masterDisplay"`
	SlaveDisplay   string `json:"slaveDisplay"`
	CurrentMaster  string `json:"currentMaster"`

	Pods []PodStatus `json:"nodes,omitempty"`
	// 使用标准的Condition结构来表示更详细的状态信息，但是较为繁琐没有实现，留待后续升级
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// 这一句是为下面的结构体生成实现runtime.Object接口所需的方法，否则就只是普通的go结构体，而不是k8s资源
// +kubebuilder:object:root=true
// 这一句是为了生成status子资源，避免spec（用户）和status（operator）在写数据时冲突（乐观锁机制）
// +kubebuilder:subresource:status

// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Master",type="string",JSONPath=".status.currentMaster"
// +kubebuilder:printcolumn:name="MasterReady",type="string",JSONPath=".status.masterDisplay"
// +kubebuilder:printcolumn:name="SlaveReady",type="string",JSONPath=".status.slaveDisplay"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
type MysqlCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MysqlClusterSpec   `json:"spec,omitempty"`
	Status MysqlClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

type MysqlClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MysqlCluster `json:"items"`
}

// 注册 Scheme，告诉runtime.Scheme，如果有一个json数据的kind是MysqlCluster或MysqlClusterList，就用上面的结构体来解析
func init() {
	SchemeBuilder.Register(&MysqlCluster{}, &MysqlClusterList{})
}
