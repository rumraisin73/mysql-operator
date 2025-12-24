package controller

import (
	"context"
	"fmt"
	"reflect"

	dbv1 "mysql-operator/api/v1"

	"sigs.k8s.io/controller-runtime/pkg/log"
)

// 计算并更新集群状态

func (r *MysqlClusterReconciler) updateStatus(ctx context.Context, cluster *dbv1.MysqlCluster, snapshot *ClusterSnapshot) error {
	logger := log.FromContext(ctx)

	// 基础数据统计
	var (
		masterCount int32
		slaveCount  int32
		masterNode  *PodInfo
		podsStatus  []dbv1.PodStatus
	)

	// 遍历快照生成状态列表

	for _, pod := range snapshot.Pods {
		// 计数统计
		if pod.IsConnectable && pod.Role == "master" {
			masterCount++
			masterNode = pod
		}
		if pod.IsConnectable && pod.Role == "slave" {
			slaveCount++
		}

		podsStatus = append(podsStatus, dbv1.PodStatus{
			Name:          pod.Pod.Name,
			Role:          pod.Role,
			IsReady:       pod.IsReady,
			IsConnectable: pod.IsConnectable,
		})
	}

	// phase推导

	currentPhase := cluster.Status.Phase

	// 判断是否已经进入了“运行阶段”
	// 如果之前已经是Running,Degraded,Failed 之一，说明集群已经成年了
	desiredReplicas := int32(3)
	if cluster.Spec.Replicas != nil {
		desiredReplicas = *cluster.Spec.Replicas
	}
	var phase dbv1.MysqlClusterPhase
	isBootstrapped := currentPhase == dbv1.MysqlClusterPhaseRunning ||
		currentPhase == dbv1.MysqlClusterPhaseDegraded ||
		currentPhase == dbv1.MysqlClusterPhaseFailed

	switch {

	// Terminating
	case !cluster.DeletionTimestamp.IsZero():
		phase = dbv1.MysqlClusterPhaseTerminating

	// 运行阶段
	case isBootstrapped:
		if masterNode == nil || !masterNode.IsConnectable {
			phase = dbv1.MysqlClusterPhaseFailed
		} else if slaveCount < desiredReplicas-1 {
			phase = dbv1.MysqlClusterPhaseDegraded
		} else {
			phase = dbv1.MysqlClusterPhaseRunning
		}

	// 启动阶段
	default:
		// 晋升判定
		if masterCount >= 1 && slaveCount >= 1 {
			phase = dbv1.MysqlClusterPhaseDegraded

		} else if len(snapshot.Pods) == 0 {
			phase = dbv1.MysqlClusterPhaseInitializing
		} else {
			phase = dbv1.MysqlClusterPhasePending
		}
	}

	// 显示字段
	// 格式化为 "2/3" 的形式，优化kubectl get体验
	masterDisplay := fmt.Sprintf("%d/%d", masterCount, int32(1))
	slaveDisplay := fmt.Sprintf("%d/%d", slaveCount, len(snapshot.Pods)-1)

	currentMasterName := ""
	if masterNode != nil {
		currentMasterName = masterNode.Pod.Name
	}

	// 构建并更新status

	newStatus := dbv1.MysqlClusterStatus{
		Phase: phase,

		MasterReplicas: masterCount,
		SlaveReplicas:  slaveCount,
		MasterDisplay:  masterDisplay,
		SlaveDisplay:   slaveDisplay,
		CurrentMaster:  currentMasterName,

		// 详细列表
		Pods: podsStatus,

		// 保留原有的conditions
		Conditions: cluster.Status.Conditions,
	}

	// 只有当状态真的变了才发送请求
	if !reflect.DeepEqual(cluster.Status, newStatus) {
		cluster.Status = newStatus
		// 更新status子资源必须用r.Status().Update
		if err := r.Status().Update(ctx, cluster); err != nil {

			return fmt.Errorf("更新status失败：%w", err)
		}
		logger.Info("10.status已更新",
			"phase", phase,
			"master", currentMasterName,
			"masterDisplay", masterDisplay,
			"slaveDisplay", slaveDisplay,
		)
	}

	return nil
}
