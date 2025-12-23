package controller

import (
	"context"
	"errors"
	"fmt"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	dbv1 "mysql-operator/api/v1"
)

type MysqlClusterReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=apps.rumraisin.me,resources=mysqlclusters,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=apps.rumraisin.me,resources=mysqlclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps.rumraisin.me,resources=mysqlclusters/finalizers,verbs=update

// 增加权限
// +kubebuilder:rbac:groups=apps,resources=statefulsets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=pods;services;configmaps;secrets,verbs=get;list;watch;create;update;patch;delete

type PodInfo struct {
	Pod           *corev1.Pod
	Role          string
	IsReady       bool
	IsConnectable bool
	GTID          string
}

// 快照结构体
type ClusterSnapshot struct {
	RootPassword string
	ReplPassword string

	Pods []*PodInfo // 建议用slice，如果用map，后面的数据库并发操作会很麻烦
}

func (r *MysqlClusterReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)
	logger.Info("0.开始调谐MysqlCluster")

	// 1.获取cluster
	var cluster dbv1.MysqlCluster
	if err := r.Get(ctx, req.NamespacedName, &cluster); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	logger.Info("1.已获取MysqlCluster资源", "name", cluster.Name)

	// 2.更新phase为Initializing，提高用户体验
	if cluster.Status.Phase == "" {

		cluster.Status.Phase = dbv1.MysqlClusterPhaseInitializing
		if err := r.Status().Update(ctx, &cluster); err != nil {

			return ctrl.Result{}, fmt.Errorf("2.更新phase为Initializing状态失败: %w", err)
		}
		return ctrl.Result{}, nil
	}
	logger.Info("2.已更新phase为Initializing状态")

	// 3.获取密码，初始化快照结构体
	secretName := cluster.Spec.SecretName.Name

	rootPassword, replPassword, err := r.checkSecret(ctx, secretName, &cluster)
	if err != nil {

		return ctrl.Result{}, err
	}

	snapshot := &ClusterSnapshot{

		RootPassword: rootPassword,
		ReplPassword: replPassword,
	}
	logger.Info("3.已获取密码并初始化快照结构体")

	// 4.确保基础资源，service，configmap，statefulset
	if err := r.ensureInfrastructure(ctx, &cluster); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("4.已确保基础资源")

	// 5.更新快照，将pod信息存入
	if err := r.updateSnapshotWithPod(ctx, &cluster, snapshot); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("5.已更新快照的Pod信息")

	// 6.确保数据库能连接且有同步账号
	if err = r.ensureDatabaseUsers(ctx, snapshot); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("6.已确保数据库能连接且有同步账号")

	// 7.更新快照，将gtid信息和数据库连接状态存入
	if err := r.updateSnapshotWithGTID(ctx, snapshot); err != nil {
		logger.Info("7.部分数据库无法连接", "err", err)

		// 不返回错误，允许部分节点连接失败继续往下走
	}
	logger.Info("7.已更新快照的GTID和连接状态信息")

	// 8.选主和打标签

	changed, err := r.reconcileRoles(ctx, snapshot)

	// 如果可用pod数量不足，则更新status并5秒后重试，如果返回err会导致RequeueAfter被忽略
	if errors.Is(err, ErrHA) {

		if err := r.updateStatus(ctx, &cluster, snapshot); err != nil {
			logger.Error(err, "更新status失败")
		}
		logger.Info("8.可用节点不足，等待pod就绪", "重试时间", "5s")

		return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
	}

	if err != nil {
		return ctrl.Result{}, err
	}

	if changed {
		// 在因为label变更而提前退出前，先更新一次status
		// 这样用户能立刻看到master或者phase变了
		if err := r.updateStatus(ctx, &cluster, snapshot); err != nil {
			logger.Error(err, "更新status失败")
		}

		logger.Info("8.角色标签变更，重新调谐")
		return ctrl.Result{}, nil
	}
	logger.Info("8.已完成选主和打标签")

	// 9.数据库内部设置修正
	if err := r.reconcileDatabaseSettings(ctx, snapshot, &cluster); err != nil {

		return ctrl.Result{}, err
	}
	logger.Info("9.已完成数据库内部设置修正")

	// 10.更新status
	if err := r.updateStatus(ctx, &cluster, snapshot); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("10.已更新status")

	// 增加1分钟的重试间隔，增强自我修复能力
	return ctrl.Result{RequeueAfter: 1 * time.Minute}, nil
}

func (r *MysqlClusterReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&dbv1.MysqlCluster{}).
		// 监听MysqlCluster所属相关的资源变化
		// 使用statefulset来控制pod就不需要监听endpoinrt，作为更高级的抽象，statefulset的改变通常比endpoint更慢，调谐发生时endpoint已经变更完毕
		Owns(&appsv1.StatefulSet{}).
		Owns(&corev1.Service{}).
		Owns(&corev1.ConfigMap{}).
		// 监听Pod变化
		// 保险措施：假如有人改了pod的标签，也能触发调谐，这种情况statefulset的状态不会变更
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(r.findObjectsForPod),
			builder.WithPredicates(predicate.ResourceVersionChangedPredicate{}),
		).
		Complete(r)
}

// 通过pod找到MysqlCluster并发起调谐请求
func (r *MysqlClusterReconciler) findObjectsForPod(ctx context.Context, pod client.Object) []reconcile.Request {

	labels := pod.GetLabels()
	clusterName, exists := labels["app"]
	if !exists {
		return []reconcile.Request{}
	}

	// 发起针对该cluster的调谐请求
	return []reconcile.Request{
		{NamespacedName: types.NamespacedName{
			Name:      clusterName,
			Namespace: pod.GetNamespace(),
		}},
	}
}
