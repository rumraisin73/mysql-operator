package controller

import (
	"context"
	"fmt"
	dbv1 "mysql-operator/api/v1"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// 填充pod信息到快照

func (r *MysqlClusterReconciler) updateSnapshotWithPod(ctx context.Context, cluster *dbv1.MysqlCluster, snapshot *ClusterSnapshot) error {

	// 获取pod列表
	podList := &corev1.PodList{}
	listOpts := []client.ListOption{
		client.InNamespace(cluster.Namespace),
		client.MatchingLabels{"app": cluster.Name},
	}

	if err := r.List(ctx, podList, listOpts...); err != nil {
		return fmt.Errorf("5.查询pod列表失败: %w", err)
	}

	// 按照pod名字排序，保证顺序固定，用于选主时防抖动

	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].Name < podList.Items[j].Name
	})

	snapshot.Pods = make([]*PodInfo, len(podList.Items))

	// podList.Items是值切片而不是指针切片，不能简单使用for _, pod := range
	for i := range podList.Items {
		// 取地址，防止range变量复用问题，也可以避免拷贝
		pod := &podList.Items[i]

		// 获取role标签
		role := ""
		if val, ok := pod.Labels["role"]; ok {
			role = val
		}
		// 填充信息
		snapshot.Pods[i] = &PodInfo{
			Pod:           pod,
			Role:          role,
			IsReady:       isPodReady(pod),
			IsConnectable: false,
			GTID:          "",
		}
	}

	return nil
}

// 判断pod是否ready，不仅要看phase是running，还要看conditons里的ready
func isPodReady(pod *corev1.Pod) bool {

	// 优化点：如果pod进入Terminating状态，它有可能还是running且ready的，会影响故障切换速度
	if pod.DeletionTimestamp != nil {
		return false
	}

	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady && cond.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
